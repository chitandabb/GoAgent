package config

import (
	"strings"
	"testing"
)

func TestRabbitMQConfigValidate(t *testing.T) {
	valid := RabbitMQConfig{
		Enabled: true, URLEnv: "MESGUARD_RABBITMQ_URL", Exchange: "mesguard.tasks",
		DiagnosisQueue: "mesguard.diagnosis.execute", DiagnosisRoutingKey: "diagnosis.execute",
		RelayBatchSize: 10, RelayPollIntervalMillis: 1000, RelayLeaseMillis: 300000,
		PublishConfirmTimeoutMillis: 5000,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}

	invalid := valid
	invalid.RelayLeaseMillis = 50000
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "relayLeaseMillis") {
		t.Fatalf("Validate() error = %v, want relay lease error", err)
	}
}

func TestRabbitMQConfigURLRejectsNonAMQPScheme(t *testing.T) {
	t.Setenv("MESGUARD_RABBITMQ_URL_TEST", "https://rabbitmq.example.test")
	config := RabbitMQConfig{URLEnv: "MESGUARD_RABBITMQ_URL_TEST"}
	if _, err := config.URL(); err == nil {
		t.Fatal("URL() accepted non-AMQP scheme")
	}
}
