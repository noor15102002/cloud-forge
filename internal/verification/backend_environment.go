package verification

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"

	"github.com/noor15102002/cloud-forge/internal/dependency"
	"github.com/noor15102002/cloud-forge/pkg/model"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const applicationSecretName = "cf-application-configuration"

// #nosec G101 -- Kubernetes object name only; passwords are generated at runtime.
const postgresSecretName = "cf-postgres-credentials"

// generatedEnvironment is called only after runtime prerequisites pass. Values
// never enter the public plan, fingerprint, command arguments or diagnostics.
func generatedEnvironment(config model.RuntimeConfiguration, runID string) ([]any, error) {
	labels := map[string]string{"app.kubernetes.io/managed-by": "cloudforge", "cloudforge.dev/run-id": runID}
	objects := []any{&corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: labels}}}
	postgresURL := ""
	if config.Dependencies["postgresql"].Enabled {
		password, err := generateValue(model.GeneratedValueSpec{Bytes: 32, Encoding: "hex"})
		if err != nil {
			return nil, err
		}
		objects = append(objects, &corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{Name: postgresSecretName, Namespace: namespace, Labels: labels}, Type: corev1.SecretTypeOpaque, StringData: map[string]string{"username": "cloudforge", "database": "cloudforge", "password": password}})
		endpoint := url.URL{Scheme: "postgresql", User: url.UserPassword("cloudforge", password), Host: dependency.PostgresName + "." + namespace + ".svc.cluster.local:5432", Path: "/cloudforge", RawQuery: "sslmode=disable"}
		postgresURL = endpoint.String()
	}
	values := map[string]string{}
	for name, binding := range config.Environment {
		var value string
		switch {
		case binding.Generate != nil:
			var err error
			value, err = generateValue(*binding.Generate)
			if err != nil {
				return nil, err
			}
		case binding.Value != nil:
			value = *binding.Value
		default:
			switch binding.From {
			case "dependency.redis.url":
				value = dependency.RedisURL(namespace)
			case "dependency.postgresql.url":
				value = postgresURL
			case "dependency.clamav.host":
				value = dependency.ClamAVName + "." + namespace + ".svc.cluster.local"
			case "dependency.clamav.port":
				value = "3310"
			case "disabled.http_url":
				value = "http://disabled.invalid"
			case "disabled.https_url":
				value = "https://disabled.invalid"
			case "disabled.host":
				value = "disabled.invalid"
			case "disabled.email":
				value = "cloudforge@disabled.invalid"
			default:
				return nil, errors.New("unsupported test environment binding")
			}
		}
		values[name] = value
	}
	if len(values) > 0 {
		objects = append(objects, &corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{Name: applicationSecretName, Namespace: namespace, Labels: labels}, Type: corev1.SecretTypeOpaque, StringData: values})
	}
	return objects, nil
}

func generateValue(spec model.GeneratedValueSpec) (string, error) {
	if spec.Bytes < 16 || spec.Bytes > 64 {
		return "", errors.New("invalid generated value size")
	}
	value := make([]byte, spec.Bytes)
	if _, err := rand.Read(value); err != nil {
		return "", errors.New("test credential generation failed")
	}
	switch spec.Encoding {
	case "hex":
		return hex.EncodeToString(value), nil
	case "base64":
		return base64.StdEncoding.EncodeToString(value), nil
	default:
		return "", errors.New("invalid generated value encoding")
	}
}

func enabledProviders(config model.RuntimeConfiguration) []string {
	result := []string{}
	for _, name := range []string{"clamav", "postgresql", "redis"} {
		if config.Dependencies[name].Enabled {
			result = append(result, name)
		}
	}
	return result
}

func providerFingerprint(name string) model.DependencyFingerprint {
	switch name {
	case "postgresql":
		return dependency.PostgresFingerprint()
	case "clamav":
		return dependency.ClamAVFingerprint()
	default:
		return dependency.RedisFingerprint()
	}
}

func providerName(name string) string {
	switch name {
	case "postgresql":
		return dependency.PostgresName
	case "clamav":
		return dependency.ClamAVName
	default:
		return dependency.RedisName
	}
}

func runIdentifier(current plan) string {
	return strings.TrimPrefix(current.clusterName, "cloudforge-")
}
