package inspect

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
)

type workflow struct {
	Repository string `json:"repository"`
}

type externalParameters struct {
	Workflow workflow `json:"workflow"`
}

type githubInfo struct {
	RepositoryID      string `json:"repository_id"`
	RepositoryOwnerId string `json:"repository_owner_id"`
}

type internalParameters struct {
	GitHub githubInfo `json:"github"`
}

type buildDefinition struct {
	ExternalParameters externalParameters `json:"externalParameters"`
	InternalParameters internalParameters `json:"internalParameters"`
}

type metadata struct {
	InvocationID string `json:"invocationId"`
}

type runDetails struct {
	Metadata metadata `json:"metadata"`
}

// Predicate 捕获给定证明(attestation)的谓词(predicate)
type Predicate struct {
	BuildDefinition buildDefinition `json:"buildDefinition"`
	RunDetails      runDetails      `json:"runDetails"`
}

// AttestationDetail 捕获证明(attestation)的来源细节，
// 这些细节将由 inspect 命令返回
type AttestationDetail struct {
	OrgName        string `json:"orgName"`
	OrgID          string `json:"orgId"`
	RepositoryName string `json:"repositoryName"`
	RepositoryID   string `json:"repositoryId"`
	WorkflowID     string `json:"workflowId"`
}

func getOrgAndRepo(tenant, repoURL string) (string, string, error) {
	var after string
	var found bool
	if tenant == "" {
		after, found = strings.CutPrefix(repoURL, "https://github.com/")
		if !found {
			return "", "", fmt.Errorf("从 %s 获取组织和仓库失败", repoURL)
		}
	} else {
		after, found = strings.CutPrefix(repoURL,
			fmt.Sprintf("https://%s.ghe.com/", tenant))
		if !found {
			return "", "", fmt.Errorf("从 %s 获取组织和仓库失败", repoURL)
		}
	}

	parts := strings.Split(after, "/")
	return parts[0], parts[1], nil
}

func getAttestationDetail(tenant string, attr api.Attestation) (AttestationDetail, error) {
	envelope, err := attr.Bundle.Envelope()
	if err != nil {
		return AttestationDetail{}, fmt.Errorf("从签名包获取签名信封失败: %v", err)
	}

	statement, err := envelope.EnvelopeContent().Statement()
	if err != nil {
		return AttestationDetail{}, fmt.Errorf("从签名信封获取声明内容失败: %v", err)
	}

	var predicate Predicate
	predicateJson, err := json.Marshal(statement.Predicate)
	if err != nil {
		return AttestationDetail{}, fmt.Errorf("序列化断言失败: %v", err)
	}

	err = json.Unmarshal(predicateJson, &predicate)
	if err != nil {
		return AttestationDetail{}, fmt.Errorf("反序列化断言失败: %v", err)
	}

	org, repo, err := getOrgAndRepo(tenant, predicate.BuildDefinition.ExternalParameters.Workflow.Repository)
	if err != nil {
		return AttestationDetail{}, fmt.Errorf("解析证明内容失败: %v", err)
	}

	return AttestationDetail{
		OrgName:        org,
		OrgID:          predicate.BuildDefinition.InternalParameters.GitHub.RepositoryOwnerId,
		RepositoryName: repo,
		RepositoryID:   predicate.BuildDefinition.InternalParameters.GitHub.RepositoryID,
		WorkflowID:     predicate.RunDetails.Metadata.InvocationID,
	}, nil
}
