package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/veraison/corim/comid"
	"github.com/veraison/psatoken"
)

var (
	cogenKeyFile           *string
	cogenAttestationScheme *string
	cogenCorimFile         *string
	cogenEvidenceFile      *string
)

var cogenGenCmd = NewCogenGenCmd()

func NewCogenGenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gen",
		Short: "PLACEHOLDER",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkCogenGenArgs(); err != nil {
				return err
			}
			err := generate(cogenKeyFile, cogenAttestationScheme, cogenCorimFile, cogenEvidenceFile)
			if err != nil {
				return err
			}
			fmt.Printf("PLACEHOLDER")
			return nil
		},
	}

	cogenAttestationScheme = cmd.Flags().StringP("attest-scheme", "a", "", "attestation scheme used")

	cogenCorimFile = cmd.Flags().StringP("corim-files", "c", "", "name of the generated CoRIM  file")

	cogenEvidenceFile = cmd.Flags().StringP("evidence-file", "e", "", "a CBOR-encoded evidence file")

	cogenKeyFile = cmd.Flags().StringP("key-file", "k", "", "a JSON-encoded key file")

	return cmd
}

func checkCogenGenArgs() error {
	if cogenKeyFile == nil || *cogenKeyFile == "" {
		return errors.New("no key supplied")
	}

	if cogenAttestationScheme == nil || *cogenAttestationScheme == "" {
		return errors.New("no attestation scheme supplied")
	}

	if cogenEvidenceFile == nil || *cogenEvidenceFile == "" {
		return errors.New("no evidence file supplied")
	}

	return nil
}

func generate(key_file *string, attestation_scheme *string, corim_file *string, evidence_file *string) error {

	evcli_cmd := exec.Command("evcli", *attestation_scheme, "check", "--token="+*evidence_file, "--key="+*key_file, "--claims=../data/output-evidence-claims.json")
	if err := evcli_cmd.Run(); err != nil {
		return err
	}

	content, err := os.ReadFile(*evidence_file)
	if err != nil {
		return err
	}

	var evidence psatoken.Evidence

	err = evidence.FromCOSE(content)
	if err != nil {
		return err
	}

	swComponents, err := evidence.Claims.GetSoftwareComponents()
	if err != nil {
		return err
	}

	implIDByte, err := evidence.Claims.GetImplID()
	if err != nil {
		return err
	}
	var implID comid.ImplID
	copy(implID[:], implIDByte)

	measurements := comid.NewMeasurements()

	for _, component := range swComponents {
		refValID := comid.NewPSARefValID(*component.SignerID)
		refValID.SetLabel(*component.MeasurementType)
		refValID.SetVersion(*component.Version)
		measurement := comid.NewPSAMeasurement(*refValID)
		measurement.AddDigest(1, *component.MeasurementValue)
		measurements.AddMeasurement(measurement)
	}

	refVal := comid.ReferenceValue{
		Environment:  comid.Environment{Class: comid.NewClassImplID(implID)},
		Measurements: *measurements,
	}

	content, err = os.ReadFile("../data/comid-claims.json")
	if err != nil {
		return err
	}

	comidClaims := comid.NewComid()
	err = comidClaims.FromJSON(content)
	if err != nil {
		return err
	}

	referenceValues := append(*new([]comid.ReferenceValue), refVal)
	comidClaims.Triples.ReferenceValues = &referenceValues

	content, err = comidClaims.ToJSON()
	if err != nil {
		return err
	}
	os.WriteFile("../data/comid-claims.json", content, 0664)

	comid_cmd := exec.Command("cocli", "comid", "create", "--template=../data/comid-claims.json", "--output-dir=../data")
	if err := comid_cmd.Run(); err != nil {
		return err
	}

	corim_cmd := exec.Command("cocli", "corim", "create", "--template=../data/corim-full.json", "--comid=../data/comid-claims.cbor", "--output=../data/output-corim.cbor")

	if *corim_file != "" {
		corim_cmd = exec.Command("cocli", "corim", "create", "--template=../data/corim-full.json", "--comid=../data/comid-claims.cbor", "--output="+*corim_file)
	}

	if err := corim_cmd.Run(); err != nil {
		return err
	}

	return nil
}

func init() {
	cogenCmd.AddCommand(cogenGenCmd)
}
