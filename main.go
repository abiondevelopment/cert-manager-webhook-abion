package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/abiondevelopment/cert-manager-webhook-abion/internal"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/cmd"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"

	extapi "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	klog "k8s.io/klog/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var GroupName = os.Getenv("GROUP_NAME")

func main() {
	if GroupName == "" {
		panic("GROUP_NAME must be specified")
	}

	// This will register our Abion DNS provider with the webhook serving
	// library, making it available as an API under the provided GroupName.
	// You can register multiple DNS provider implementations with a single
	// webhook, where the Name() method will be used to disambiguate between
	// the different implementations.
	cmd.RunWebhookServer(GroupName,
		&abionDNSProviderSolver{},
	)
}

// abionDNSProviderSolver implements the provider-specific logic needed to
// 'present' an ACME challenge TXT record for your own DNS provider.
// To do so, it must implement the `github.com/cert-manager/cert-manager/pkg/acme/webhook.Solver`
// interface.
type abionDNSProviderSolver struct {
	client *kubernetes.Clientset
}

// abionDNSProviderConfig is a structure that is used to decode into when
// solving a DNS01 challenge.
// This information is provided by cert-manager, and may be a reference to
// additional configuration that's needed to solve the challenge for this
// particular certificate or issuer.
// This typically includes references to Secret resources containing DNS
// provider credentials, in cases where a 'multi-tenant' DNS solver is being
// created.
// If you do *not* require per-issuer or per-certificate configuration to be
// provided to your webhook, you can skip decoding altogether in favour of
// using CLI flags or similar to provide configuration.
// You should not include sensitive information here. If credentials need to
// be used by your provider here, you should reference a Kubernetes Secret
// resource and fetch these credentials using a Kubernetes clientset.
type abionDNSProviderConfig struct {
	APIKeySecretRef cmmeta.SecretKeySelector `json:"apiKeySecretRef"`
}

// Type holding credential.
type credential struct {
	ApiKey string
}

// Name is used as the name for this DNS solver when referencing it on the ACME
// Issuer resource.
// This should be unique **within the group name**, i.e. you can have two
// solvers configured with the same Name() **so long as they do not co-exist
// within a single webhook deployment**.
// For example, `cloudflare` may be used as the name of a solver.
func (c *abionDNSProviderSolver) Name() string {
	return "abion"
}

// Present is responsible for actually presenting the DNS record with the
// DNS provider.
// This method should tolerate being called multiple times with the same value.
// cert-manager itself will later perform a self check to ensure that the
// solver has correctly configured the DNS provider.
func (c *abionDNSProviderSolver) Present(ch *v1alpha1.ChallengeRequest) error {
	klog.V(6).Infof("call function Present: namespace=%s, zone=%s, fqdn=%s",
		ch.ResourceNamespace, ch.ResolvedZone, ch.ResolvedFQDN)

	cfg, err := loadConfig(ch.Config)
	if err != nil {
		return err
	}
	klog.V(6).Infof("decoded configuration %v", cfg)

	// Get credentials for connecting to Abion API.
	credentials, err := c.getCredentials(&cfg, ch.ResourceNamespace)
	if err != nil {
		return fmt.Errorf("unable to get credential: %v", err)
	}

	// Initialize new Abion client.
	abionClient := internal.NewAbionClient(credentials.ApiKey)

	// Split and format domain and subdomain values.
	domain, subdomain := c.getDomainAndSubdomain(ch)

	zone, err := abionClient.GetZone(context.Background(), domain)
	if err != nil {
		return fmt.Errorf("unable to get zone: %v", err)
	}

	var data []internal.Record
	sub, subdomainExist := zone.Data.Attributes.Records[subdomain] // subdomain, e.g. _acme-challenge
	if subdomainExist {
		txtRecords, txtRecordsExist := sub["TXT"]
		if txtRecordsExist {
			data = append(data, txtRecords...)
		}
	}

	// append the new dns-01 acme challenge record
	data = append(data, internal.Record{
		TTL:      60,
		Data:     ch.Key,
		Comments: "acme_challenge",
	})

	patchRequest := internal.ZoneRequest{
		Data: internal.Zone{
			Type: "zone",
			ID:   domain,
			Attributes: internal.Attributes{
				Records: map[string]map[string][]internal.Record{
					subdomain: {"TXT": data},
				},
			},
		},
	}

	_, err = abionClient.PatchZone(context.Background(), domain, patchRequest)
	if err != nil {
		return fmt.Errorf("error updating zone %w", err)
	}

	return nil
}

// CleanUp should delete the relevant TXT record from the DNS provider console.
// If multiple TXT records exist with the same record name (e.g.
// _acme-challenge.example.com) then **only** the record with the same `key`
// value provided on the ChallengeRequest should be cleaned up.
// This is in order to facilitate multiple DNS validations for the same domain
// concurrently.
func (c *abionDNSProviderSolver) CleanUp(ch *v1alpha1.ChallengeRequest) error {
	klog.V(6).Infof("call function CleanUp: namespace=%s, zone=%s, fqdn=%s",
		ch.ResourceNamespace, ch.ResolvedZone, ch.ResolvedFQDN)

	cfg, err := loadConfig(ch.Config)
	if err != nil {
		return err
	}

	klog.V(6).Infof("decoded configuration %v", cfg)

	// Get credentials for connecting to Abion.
	credentials, err := c.getCredentials(&cfg, ch.ResourceNamespace)
	if err != nil {
		return fmt.Errorf("unable to get credential: %v", err)
	}

	// Initialize new Abion client.
	abionClient := internal.NewAbionClient(credentials.ApiKey)

	// Split and format domain and subdomain values.
	domain, subdomain := c.getDomainAndSubdomain(ch)

	zone, err := abionClient.GetZone(context.Background(), domain)
	if err != nil {
		return fmt.Errorf("unable to get zone: %v", err)
	}

	var data []internal.Record
	sub, subdomainExist := zone.Data.Attributes.Records[subdomain] // subdomain, e.g. _acme-challenge
	if subdomainExist {
		txtRecords, txtRecordsExist := sub["TXT"]
		if txtRecordsExist {
			for _, record := range txtRecords {
				if record.Data != ch.Key {
					data = append(data, record)
				}
			}
		}
	}

	payload := map[string][]internal.Record{}
	if len(data) == 0 {
		payload["TXT"] = nil
	} else {
		payload["TXT"] = data
	}

	patchRequest := internal.ZoneRequest{
		Data: internal.Zone{
			Type: "zone",
			ID:   domain,
			Attributes: internal.Attributes{
				Records: map[string]map[string][]internal.Record{
					subdomain: payload,
				},
			},
		},
	}

	_, err = abionClient.PatchZone(context.Background(), domain, patchRequest)
	if err != nil {
		return fmt.Errorf("error updating zone %w", err)
	}

	return nil
}

// Initialize will be called when the webhook first starts.
// This method can be used to instantiate the webhook, i.e. initialising
// connections or warming up caches.
// Typically, the kubeClientConfig parameter is used to build a Kubernetes
// client that can be used to fetch resources from the Kubernetes API, e.g.
// Secret resources containing credentials used to authenticate with DNS
// provider accounts.
// The stopCh can be used to handle early termination of the webhook, in cases
// where a SIGTERM or similar signal is sent to the webhook process.
func (c *abionDNSProviderSolver) Initialize(kubeClientConfig *rest.Config, stopCh <-chan struct{}) error {
	cl, err := kubernetes.NewForConfig(kubeClientConfig)
	if err != nil {
		return err
	}

	c.client = cl

	return nil
}

// Split and format domain and subdomain values.
func (c *abionDNSProviderSolver) getDomainAndSubdomain(ch *v1alpha1.ChallengeRequest) (string, string) {
	// ch.ResolvedZone form: abion.com.
	// ch.ResolvedFQDN form:  _acme-challenge.abion.com.
	domain := strings.TrimSuffix(ch.ResolvedZone, ".")
	subDomain := strings.TrimSuffix(ch.ResolvedFQDN, ch.ResolvedZone)
	// Trim trailing dots
	subDomain = strings.TrimSuffix(subDomain, ".")

	return domain, subDomain
}

// loadConfig is a small helper function that decodes JSON configuration into
// the typed config struct.
func loadConfig(cfgJSON *extapi.JSON) (abionDNSProviderConfig, error) {
	cfg := abionDNSProviderConfig{}
	// handle the 'base case' where no configuration has been provided
	if cfgJSON == nil {
		return cfg, nil
	}
	if err := json.Unmarshal(cfgJSON.Raw, &cfg); err != nil {
		return cfg, fmt.Errorf("error decoding solver config: %v", err)
	}

	return cfg, nil
}

// Get Abion API credentials from Kubernetes secret.
func (c *abionDNSProviderSolver) getCredentials(cfg *abionDNSProviderConfig, namespace string) (*credential, error) {
	credentials := credential{}

	// Get API Key.
	klog.V(2).Infof("Trying to load secret `%s` with key `%s`", cfg.APIKeySecretRef.Name, cfg.APIKeySecretRef.Key)
	apiKeySecret, err := c.client.CoreV1().Secrets(namespace).Get(context.Background(), cfg.APIKeySecretRef.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to load secret %q: %s", namespace+"/"+cfg.APIKeySecretRef.Name, err.Error())
	}
	if apiKey, ok := apiKeySecret.Data[cfg.APIKeySecretRef.Key]; ok {
		credentials.ApiKey = string(apiKey)
	} else {
		return nil, fmt.Errorf("error fetching key from secrets")
	}

	return &credentials, nil
}
