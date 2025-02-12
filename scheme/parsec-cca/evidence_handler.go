// Copyright 2023 Contributors to the Veraison project.
// SPDX-License-Identifier: Apache-2.0
package parsec_cca

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/veraison/ear"
	"github.com/veraison/go-cose"
	parsec_cca "github.com/veraison/parsec/cca"
	"github.com/veraison/psatoken"
	"github.com/veraison/services/handler"
	"github.com/veraison/services/log"
	"github.com/veraison/services/proto"
	"github.com/veraison/services/scheme/common"
	"github.com/veraison/services/scheme/common/arm"
)

const (
	ScopeTrustAnchor = "trust anchor"
	ScopeRefValues   = "ref values"
)

type EvidenceHandler struct{}

func (s EvidenceHandler) GetName() string {
	return "parsec-cca-evidence-handler"
}

func (s EvidenceHandler) GetAttestationScheme() string {
	return SchemeName
}

func (s EvidenceHandler) GetSupportedMediaTypes() []string {
	return EvidenceMediaTypes
}

func (s EvidenceHandler) SynthKeysFromRefValue(
	tenantID string,
	refVal *handler.Endorsement,
) ([]string, error) {

	return arm.SynthKeysFromRefValue(SchemeName, tenantID, refVal)
}

func (s EvidenceHandler) SynthKeysFromTrustAnchor(tenantID string, ta *handler.Endorsement) ([]string, error) {

	return arm.SynthKeysFromTrustAnchors(SchemeName, tenantID, ta)
}

func (s EvidenceHandler) GetTrustAnchorID(token *proto.AttestationToken) (string, error) {
	return arm.GetTrustAnchorID(SchemeName, token)
}

func (s EvidenceHandler) ExtractClaims(token *proto.AttestationToken, trustAnchor string) (*handler.ExtractedClaims, error) {
	var (
		extracted handler.ExtractedClaims
		evidence  parsec_cca.Evidence
		claimsSet = make(map[string]interface{})
		kat       = make(map[string]interface{})
	)

	if err := evidence.FromCBOR(token.Data); err != nil {
		return nil, handler.BadEvidence(err)
	}
	kat["nonce"] = *evidence.Kat.Nonce
	key := evidence.Kat.Cnf.COSEKey
	ck, err := key.MarshalCBOR()
	if err != nil {
		return nil, handler.BadEvidence(err)
	}

	kat["akpub"] = base64.StdEncoding.EncodeToString(ck)

	claimsSet["kat"] = kat
	pmap, err := common.ClaimsToMap(evidence.Pat.PlatformClaims)
	if err != nil {
		return nil, handler.BadEvidence(err)
	}
	claimsSet["cca.platform"] = pmap
	rmap, err := common.ClaimsToMap(evidence.Pat.RealmClaims)
	if err != nil {
		return nil, handler.BadEvidence(err)
	}
	claimsSet["cca.realm"] = rmap

	extracted.ClaimsSet = claimsSet

	extracted.ReferenceID = arm.RefValLookupKey(
		SchemeName,
		token.TenantId,
		arm.MustImplIDString(evidence.Pat.PlatformClaims),
	)
	log.Debugf("extracted Reference ID Key = %s", extracted.ReferenceID)
	return &extracted, nil
}

func (s EvidenceHandler) ValidateEvidenceIntegrity(token *proto.AttestationToken, trustAnchor string, endorsements []string) error {
	var (
		evidence parsec_cca.Evidence
	)

	if err := evidence.FromCBOR(token.Data); err != nil {
		return handler.BadEvidence(err)
	}

	pk, err := arm.GetPublicKeyFromTA(SchemeName, trustAnchor)
	if err != nil {
		return fmt.Errorf("could not get public key from trust anchor: %w", err)
	}

	if err = evidence.Verify(pk); err != nil {
		return fmt.Errorf("failed to verify signature: %w", err)
	}
	log.Debug("Parsec CCA token signature, verified")
	return nil
}

func (s EvidenceHandler) AppraiseEvidence(
	ec *proto.EvidenceContext, endorsementsStrings []string,
) (*ear.AttestationResult, error) {
	var endorsements []handler.Endorsement // nolint:prealloc

	result := handler.CreateAttestationResult(SchemeName)

	for i, e := range endorsementsStrings {
		var endorsement handler.Endorsement

		if err := json.Unmarshal([]byte(e), &endorsement); err != nil {
			return nil, fmt.Errorf("could not decode endorsement at index %d: %w", i, err)
		}

		endorsements = append(endorsements, endorsement)
	}

	err := populateAttestationResult(result, ec.Evidence.AsMap(), endorsements)
	return result, err
}

func populateAttestationResult(
	result *ear.AttestationResult,
	evidence map[string]interface{},
	endorsements []handler.Endorsement,
) error {
	appraisal := result.Submods[SchemeName]

	// once the signature on the token is verified, we can claim the HW is
	// authentic
	appraisal.TrustVector.Hardware = ear.GenuineHardwareClaim
	kmap, ok := evidence["kat"]
	if !ok {
		return handler.BadEvidence(errors.New("no key attestation map in the evidence"))
	}
	kat := kmap.(map[string]interface{})

	key, ok := kat["akpub"]
	if !ok {
		return handler.BadEvidence(errors.New("no key in the evidence"))
	}
	var COSEKey cose.Key

	kb, err := base64.StdEncoding.DecodeString(key.(string))
	if err != nil {
		return handler.BadEvidence(err)
	}
	err = COSEKey.UnmarshalCBOR(kb)
	if err != nil {
		return handler.BadEvidence(err)
	}
	// Extract Public Key and set the Veraison Extension
	pk, err := COSEKey.PublicKey()
	if err != nil {
		return handler.BadEvidence(err)
	}

	if err := appraisal.SetKeyAttestation(pk); err != nil {
		return fmt.Errorf("setting extracted public key: %w", err)
	}

	cp, ok := evidence["cca.platform"]
	if !ok {
		return handler.BadEvidence(errors.New("no cca platform in the evidence"))
	}
	pmap := cp.(map[string]interface{})
	claims, err := common.MapToClaims(pmap)
	if err != nil {
		return handler.BadEvidence(err)
	}

	rawLifeCycle, err := claims.GetSecurityLifeCycle()
	if err != nil {
		return handler.BadEvidence(err)
	}

	lifeCycle := psatoken.CcaLifeCycleToState(rawLifeCycle)
	if lifeCycle == psatoken.CcaStateSecured ||
		lifeCycle == psatoken.CcaStateNonCcaPlatformDebug {
		appraisal.TrustVector.InstanceIdentity = ear.TrustworthyInstanceClaim
		appraisal.TrustVector.RuntimeOpaque = ear.ApprovedRuntimeClaim
		appraisal.TrustVector.StorageOpaque = ear.HwKeysEncryptedSecretsClaim
	} else {
		appraisal.TrustVector.InstanceIdentity = ear.UntrustworthyInstanceClaim
		appraisal.TrustVector.RuntimeOpaque = ear.VisibleMemoryRuntimeClaim
		appraisal.TrustVector.StorageOpaque = ear.UnencryptedSecretsClaim
	}

	swComps := arm.FilterRefVal(endorsements, "PARSEC_CCA.sw-component")
	match := arm.MatchSoftware(SchemeName, claims, swComps)
	if match {
		appraisal.TrustVector.Executables = ear.ApprovedRuntimeClaim
		log.Debug("matchSoftware Success")

	} else {
		appraisal.TrustVector.Executables = ear.UnrecognizedRuntimeClaim
		log.Debug("matchSoftware Failed")
	}

	platformConfig := arm.FilterRefVal(endorsements, "PARSEC_CCA.platform-config")
	match = arm.MatchPlatformConfig(SchemeName, claims, platformConfig)

	if match {
		appraisal.TrustVector.Configuration = ear.ApprovedConfigClaim
		log.Debug("matchPlatformConfig Success")

	} else {
		appraisal.TrustVector.Configuration = ear.UnsafeConfigClaim
		log.Debug("matchPlatformConfig Failed")
	}
	appraisal.UpdateStatusFromTrustVector()

	appraisal.VeraisonAnnotatedEvidence = &evidence
	return nil
}
