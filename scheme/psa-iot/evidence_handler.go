// Copyright 2021-2023 Contributors to the Veraison project.
// SPDX-License-Identifier: Apache-2.0
package psa_iot

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"

	"github.com/veraison/ear"
	"github.com/veraison/psatoken"
	"github.com/veraison/services/handler"
	"github.com/veraison/services/proto"
	"github.com/veraison/services/scheme/common"
	"github.com/veraison/services/scheme/common/arm"
)

type SwAttr struct {
	ImplID    *[]byte `json:"PSA_IOT.impl-id"`
	Model     *string `json:"PSA_IOT.hw-model"`
	Vendor    *string `json:"PSA_IOT.hw-vendor"`
	MeasDesc  *string `json:"PSA_IOT.measurement-desc"`
	MeasType  *string `json:"PSA_IOT.measurement-type"`
	MeasValue *[]byte `json:"PSA_IOT.measurement-value"`
	SignerID  *[]byte `json:"PSA_IOT.signer-id"`
	Version   *string `json:"PSA_IOT.version"`
}

type Endorsements struct {
	Scheme  string `json:"scheme"`
	Type    string `json:"type"`
	SubType string `json:"sub_type"`
	Attr    SwAttr `json:"attributes"`
}

type TaAttr struct {
	Model    *string `json:"PSA_IOT.hw-model"`
	Vendor   *string `json:"PSA_IOT.hw-vendor"`
	VerifKey *string `json:"PSA_IOT.iak-pub"`
	ImplID   *[]byte `json:"PSA_IOT.impl-id"`
	InstID   *string `json:"PSA_IOT.inst-id"`
}

type TaEndorsements struct {
	Scheme  string `json:"scheme"`
	Type    string `json:"type"`
	SubType string `json:"sub_type"`
	Attr    TaAttr `json:"attributes"`
}

type EvidenceHandler struct{}

func (s EvidenceHandler) GetName() string {
	return "psa-evidence-handler"
}

func (s EvidenceHandler) GetAttestationScheme() string {
	return SchemeName
}

func (s EvidenceHandler) GetSupportedMediaTypes() []string {
	return EvidenceMediaTypes
}

func (s EvidenceHandler) SynthKeysFromRefValue(
	tenantID string,
	refValue *handler.Endorsement,
) ([]string, error) {

	implID, err := common.GetImplID("PSA_IOT", refValue.Attributes)
	if err != nil {
		return nil, fmt.Errorf("unable to synthesize trust anchor abs-path: %w", err)
	}

	finalstr := arm.RefValLookupKey(SchemeName, tenantID, implID)
	log.Printf("PSA Plugin PSA Look Up Key= %s\n", finalstr)
	return []string{arm.RefValLookupKey(SchemeName, tenantID, implID)}, nil
}

func (s EvidenceHandler) SynthKeysFromTrustAnchor(tenantID string, ta *handler.Endorsement) ([]string, error) {

	implID, err := common.GetImplID("PSA_IOT", ta.Attributes)
	if err != nil {
		return nil, fmt.Errorf("unable to synthesize trust anchor abs-path: %w", err)
	}

	instID, err := common.GetInstID("PSA_IOT", ta.Attributes)
	if err != nil {
		return nil, fmt.Errorf("unable to synthesize trust anchor abs-path: %w", err)
	}

	finalstr := arm.TaLookupKey(SchemeName, tenantID, implID, instID)
	log.Printf("PSA Plugin TA PSA Look Up Key= %s\n", finalstr)
	return []string{arm.TaLookupKey(SchemeName, tenantID, implID, instID)}, nil
}

func (s EvidenceHandler) GetTrustAnchorID(token *proto.AttestationToken) (string, error) {
	var psaToken psatoken.Evidence

	err := psaToken.FromCOSE(token.Data)
	if err != nil {
		return "", handler.BadEvidence(err)
	}

	return arm.TaLookupKey(
		SchemeName,
		token.TenantId,
		arm.MustImplIDString(psaToken.Claims),
		arm.MustInstIDString(psaToken.Claims),
	), nil
}

func (s EvidenceHandler) ExtractClaims(
	token *proto.AttestationToken,
	trustAnchor string,
) (*handler.ExtractedClaims, error) {
	var psaToken psatoken.Evidence

	if err := psaToken.FromCOSE(token.Data); err != nil {
		return nil, handler.BadEvidence(err)
	}

	var extracted handler.ExtractedClaims

	claimsSet, err := common.ClaimsToMap(psaToken.Claims)
	if err != nil {
		return nil, handler.BadEvidence(err)
	}
	extracted.ClaimsSet = claimsSet

	extracted.ReferenceID = arm.RefValLookupKey(
		SchemeName,
		token.TenantId,
		arm.MustImplIDString(psaToken.Claims),
	)
	log.Printf("\n Extracted SW ID Key = %s", extracted.ReferenceID)
	return &extracted, nil
}

func (s EvidenceHandler) ValidateEvidenceIntegrity(
	token *proto.AttestationToken,
	trustAnchor string,
	endorsementsStrings []string,
) error {
	var (
		endorsement TaEndorsements
		psaToken    psatoken.Evidence
	)

	if err := psaToken.FromCOSE(token.Data); err != nil {
		return handler.BadEvidence(err)
	}

	if err := json.Unmarshal([]byte(trustAnchor), &endorsement); err != nil {
		log.Println("Could not decode trust anchor in ValidateEvidenceIntegrity")
		return fmt.Errorf("could not decode trust anchor: %w", err)
	}

	ta := *endorsement.Attr.VerifKey
	pk, err := common.DecodePemSubjectPubKeyInfo([]byte(ta))
	if err != nil {
		return fmt.Errorf("could not get public key from trust anchor: %w", err)
	}

	if err = psaToken.Verify(pk); err != nil {
		return handler.BadEvidence(err)
	}
	log.Println("\n Token Signature Verified")
	return nil
}

func (s EvidenceHandler) AppraiseEvidence(
	ec *proto.EvidenceContext, endorsementsStrings []string,
) (*ear.AttestationResult, error) {
	var endorsements []Endorsements // nolint:prealloc

	result := handler.CreateAttestationResult(SchemeName)

	for i, e := range endorsementsStrings {
		var endorsement Endorsements

		if err := json.Unmarshal([]byte(e), &endorsement); err != nil {
			return nil, fmt.Errorf("could not decode endorsement at index %d: %w", i, err)
		}

		endorsements = append(endorsements, endorsement)
	}

	err := populateAttestationResult(result, ec.Evidence.AsMap(), endorsements)

	return result, err
}

func mapToClaims(in map[string]interface{}) (psatoken.IClaims, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}

	return psatoken.DecodeJSONClaims(data)
}

func populateAttestationResult(
	result *ear.AttestationResult,
	evidence map[string]interface{},
	endorsements []Endorsements,
) error {
	claims, err := mapToClaims(evidence)
	if err != nil {
		return handler.BadEvidence(err)
	}

	appraisal := result.Submods[SchemeName]

	// once the signature on the token is verified, we can claim the HW is
	// authentic
	appraisal.TrustVector.Hardware = ear.GenuineHardwareClaim

	rawLifeCycle, err := claims.GetSecurityLifeCycle()
	if err != nil {
		return handler.BadEvidence(err)
	}

	lifeCycle := psatoken.PsaLifeCycleToState(rawLifeCycle)
	if lifeCycle == psatoken.PsaStateSecured || lifeCycle == psatoken.PsaStateNonPsaRotDebug {
		appraisal.TrustVector.InstanceIdentity = ear.TrustworthyInstanceClaim
		appraisal.TrustVector.RuntimeOpaque = ear.ApprovedRuntimeClaim
		appraisal.TrustVector.StorageOpaque = ear.HwKeysEncryptedSecretsClaim
	} else {
		appraisal.TrustVector.InstanceIdentity = ear.UntrustworthyInstanceClaim
		appraisal.TrustVector.RuntimeOpaque = ear.VisibleMemoryRuntimeClaim
		appraisal.TrustVector.StorageOpaque = ear.UnencryptedSecretsClaim
	}

	match := matchSoftware(claims, endorsements)
	if match {
		appraisal.TrustVector.Executables = ear.ApprovedRuntimeClaim
		log.Println("\n matchSoftware Success")

	} else {
		appraisal.TrustVector.Executables = ear.UnrecognizedRuntimeClaim
		log.Println("\n matchSoftware Failed")
	}

	appraisal.UpdateStatusFromTrustVector()

	appraisal.VeraisonAnnotatedEvidence = &evidence

	return nil
}

func matchSoftware(evidence psatoken.IClaims, endorsements []Endorsements) bool {
	evidenceComponents := make(map[string]psatoken.SwComponent)

	refValues, err := evidence.GetSoftwareComponents()
	if err != nil {
		return false
	}

	for _, c := range refValues {
		key := base64.StdEncoding.EncodeToString(*c.MeasurementValue)
		evidenceComponents[key] = c
	}
	matched := false
	for _, endorsement := range endorsements {
		// If we have Endorsements we assume they match to begin with
		matched = true
		key := base64.StdEncoding.EncodeToString(*endorsement.Attr.MeasValue)
		evComp, ok := evidenceComponents[key]
		if !ok {
			matched = false
			break
		}

		log.Printf("MeasType Evidence: %s, Endorsement: %s", *evComp.MeasurementType, *endorsement.Attr.MeasType)
		typeMatched := *endorsement.Attr.MeasType == "" || *endorsement.Attr.MeasType == *evComp.MeasurementType
		sigMatched := *endorsement.Attr.SignerID == nil || bytes.Equal(*endorsement.Attr.SignerID, *evComp.SignerID)
		versionMatched := *endorsement.Attr.Version == "" || *endorsement.Attr.Version == *evComp.Version

		if !(typeMatched && sigMatched && versionMatched) {
			matched = false
			break
		}
	}
	return matched
}
