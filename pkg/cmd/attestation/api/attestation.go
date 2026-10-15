package api

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sigstore/sigstore-go/pkg/bundle"
)

var ErrNoAttestationsFound = errors.New("未找到任何证明")

type Attestation struct {
	Bundle    *bundle.Bundle `json:"bundle"`
	BundleURL string         `json:"bundle_url"`
	Initiator string         `json:"initiator"`
}

type AttestationsResponse struct {
	Attestations []*Attestation `json:"attestations"`
}

type IntotoStatement struct {
	PredicateType string `json:"predicateType"`
}

func FilterAttestations(predicateType string, attestations []*Attestation) ([]*Attestation, error) {
	filteredAttestations := []*Attestation{}

	for _, each := range attestations {
		dsseEnvelope := each.Bundle.GetDsseEnvelope()
		if dsseEnvelope != nil {
			if dsseEnvelope.PayloadType != "application/vnd.in-toto+json" {
				// 不要因为某条记录不是 intoto 格式而失败
				continue
			}
			var intotoStatement IntotoStatement
			if err := json.Unmarshal([]byte(dsseEnvelope.Payload), &intotoStatement); err != nil {
				// 不要因为单条记录反序列化失败而失败
				continue
			}
			if intotoStatement.PredicateType == predicateType {
				filteredAttestations = append(filteredAttestations, each)
			}
		}
	}

	if len(filteredAttestations) == 0 {
		return nil, fmt.Errorf("未找到具有谓词类型的证明: %s", predicateType)
	}

	return filteredAttestations, nil
}
