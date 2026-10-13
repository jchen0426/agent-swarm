package baseline_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/bench/itbench/baseline"
	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/score"
)

func str(s string) *string { return &s }

func at(minute int) time.Time { return time.Date(2025, 12, 15, 17, minute, 0, 0, time.UTC) }

var (
	flagChange = dataset.Change{Time: at(48), Kind: "ConfigMap", Name: "flagd-config", Service: "flagd", Action: "updated",
		Fields: []dataset.Field{{Path: `data["demo.flagd.json"].flags.adFailure.defaultVariant`, From: str("off"), To: str("on")}}}
	restart = dataset.Change{Time: at(49), Kind: "Deployment", Name: "ad", Service: "ad", Action: "updated",
		Fields: []dataset.Field{{Path: `spec.template.metadata.annotations["kubectl.kubernetes.io/restartedAt"]`, To: str("2025-12-15T17:49:00Z")}}}
	envChange = dataset.Change{Time: at(50), Kind: "Deployment", Name: "shipping", Service: "shipping", Action: "updated",
		Fields: []dataset.Field{{Path: "spec.template.spec.containers[shipping].env[QUOTE_ADDR].value", From: str("quote:8080"), To: str("quote:0000")}}}
	imageChange = dataset.Change{Time: at(51), Kind: "Deployment", Name: "checkout", Service: "checkout", Action: "updated",
		Fields: []dataset.Field{{Path: "spec.template.spec.containers[checkout].image", From: str("a:1"), To: str("a:2")}}}
	quota = dataset.Change{Time: at(52), Kind: "ResourceQuota", Name: "otel-demo-memory", Action: "created"}
)

func TestEvaluateTakesTheLatestChangeInEachScope(t *testing.T) {
	d := &dataset.Dataset{
		Meta:    dataset.Meta{Trigger: []dataset.Alert{{Service: "shipping"}, {Service: "frontend"}}},
		Changes: []dataset.Change{flagChange, restart, envChange, imageChange},
	}

	service, namespace := baseline.Evaluate(d, "flagd-config")

	assert.Equal(t, &score.Key{Service: "shipping", FailureMode: "config-change"}, service)
	assert.Equal(t, &score.Key{Service: "checkout", FailureMode: "bad-release"}, namespace)
}

func TestARestartIsNotAChange(t *testing.T) {
	d := &dataset.Dataset{Meta: dataset.Meta{Trigger: []dataset.Alert{{Service: "ad"}}}, Changes: []dataset.Change{flagChange, restart}}

	service, namespace := baseline.Evaluate(d, "flagd-config")

	assert.Nil(t, service)
	assert.Equal(t, &score.Key{Service: "flagd", FailureMode: "flag-change"}, namespace)
}

func TestNoAlertingServiceMeansNoServiceAnswer(t *testing.T) {
	d := &dataset.Dataset{Meta: dataset.Meta{Trigger: []dataset.Alert{{Name: "PendingPodsDetected"}}}, Changes: []dataset.Change{envChange}}

	service, namespace := baseline.Evaluate(d, "flagd-config")

	assert.Nil(t, service)
	assert.NotNil(t, namespace)
}

func TestKeyOfMapsTheKindOfChange(t *testing.T) {
	assert.Equal(t, score.Key{Service: "flagd", FailureMode: "flag-change"}, baseline.KeyOf(&flagChange, "flagd-config"))
	assert.Equal(t, score.Key{Service: "shipping", FailureMode: "config-change"}, baseline.KeyOf(&envChange, "flagd-config"))
	assert.Equal(t, score.Key{Service: "checkout", FailureMode: "bad-release"}, baseline.KeyOf(&imageChange, "flagd-config"))
	assert.Equal(t, score.Key{Service: "other", FailureMode: "config-change"}, baseline.KeyOf(&quota, "flagd-config"))
}

func TestAlertingServicesAreTheTriggersServices(t *testing.T) {
	meta := dataset.Meta{Trigger: []dataset.Alert{{Service: "frontend"}, {Service: ""}, {Service: "checkout"}, {Service: "frontend"}}}

	assert.Equal(t, []string{"checkout", "frontend"}, baseline.AlertingServices(&meta))
}
