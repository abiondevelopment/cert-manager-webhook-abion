# ACME webhook for Abion (cert-manager-webhook-abion)
`cert-manager-webhook-abion` is an ACME webhook for [cert-manager]. It provides an ACME webhook for [cert-manager], which 
allows using a `DNS-01 challange` with [Abion]. Internally, the cert-manager-webhook-abion uses the 
Abion API to communicate with [Abion API].

## Release History
Refer to the [CHANGELOG](CHANGELOG.md) file.

## Building
Build the docker image `abiondevelopment/cert-manager-webhook-abion:latest`:

```
make build
```

## Docker images
Prebuilt docker images can be found on [Docker Hub]

## Compatibility
This webhook has been tested with [cert-manager] v1.14.4 and [minikube] v1.32.0 on Darwin 13.3 (arm64). In theory, it 
should work on other hardware platforms as well but no steps have been taken to verify this.

## Test

### Testing with Minikube
1. Build this webhook in Minikube:

   ```
   minikube start --memory=4G 
   eval $(minikube docker-env) 
   make build 
   ```

2. Install [cert-manager] with [Helm]:

   ```
   helm repo add jetstack https://charts.jetstack.io

   helm install cert-manager jetstack/cert-manager \
       --namespace cert-manager \
       --create-namespace \
       --set installCRDs=true \
       --version v1.14.4 \
       --set 'extraArgs={--dns01-recursive-nameservers=8.8.8.8:53\,1.1.1.1:53}'

   kubectl get pods --namespace cert-manager --watch
   ```
   **Note!**: refer to Name servers in the official [documentation][setting-nameservers-for-dns01-self-check] according the `extraArgs`.  

3. Check the state and ensure that all pods are running fine (watch out for any issues regarding the `cert-manager-webhook-` pod and its volume mounts):
   
   ```   
   kubectl describe pods -n cert-manager | less
   ```

4. Create the Abion API key secret in same namespace (Replace the <ABION-API-KEY> with a valid API key. You *must* have an Abion account to retrieve an API key. Contact [Abion] for help how to create an account and API key):

   ```
   kubectl create secret generic abion-credentials \
      --namespace cert-manager --from-literal=apiKey='<ABION-API-KEY>'
   ```
   **Note!** The `Secret` must reside in the same namespace as `cert-manager`.

5. Deploy the abion cert-manager-webhook (Set `logLevel` to 6 for verbose logs):

   *The `features.apiPriorityAndFairness` argument must be removed or set to `false` for Kubernetes older than 1.20.*
   ```
   helm install cert-manager-webhook-abion \
      --namespace cert-manager \
      --set features.apiPriorityAndFairness=true \
      --set image.repository=abiondevelopment/cert-manager-webhook-abion \
      --set image.tag=latest \
      --set logLevel=2 \
      ./deploy/cert-manager-webhook-abion 
   ```

   To deploy using the image from Docker Hub (for example using the `1.2.0` tag):

   ```
   helm install cert-manager-webhook-abion \
       --namespace cert-manager \
       --set features.apiPriorityAndFairness=true \
       --set image.tag=1.2.0 \
       --set logLevel=2 \
       ./deploy/cert-manager-webhook-abion
   ```

   Check the logs
   ```
   kubectl get pods --namespace cert-manager --watch
   kubectl logs --namespace cert-manager cert-manager-webhook-abion-XYZ
   ```
   
6. Create a staging cluster issuer.

   See [letsencrypt-staging-clusterissuer.yaml](example/issuers/letsencrypt-staging-clusterissuer.yaml)

   Don't forget to replace email `invalid@example.com`.
   ```
   kubectl apply -f ./example/issuers/letsencrypt-staging-clusterissuer.yaml
   ```

   Check status of the Issuer:
   ```
   kubectl describe clusterissuer letsencrypt-staging
   ```

   *Note*: The production Issuer is [similar][ACME documentation].

7. Issue a [Certificate] for your domain

   Replace dnsNames `example.com` in the [certif-example-com-clusterissuer.yaml](example/certificates/certif-example-com-clusterissuer.yaml)

   Create the Certificate:
   ```
   kubectl apply -f ./example/certificates/certif-example-com-clusterissuer.yaml
   ```
   
   Check the status of the Certificate:
   ```
   kubectl describe certificate example-com
   ```

   Display the details like the common name and subject alternative names:
   
   ```
   kubectl get secret example-com-tls -o yaml
   ```

8. Uninstall this webhook:

   ```
   helm uninstall cert-manager-webhook-abion --namespace cert-manager
   kubectl delete secret abion-credentials --namespace cert-manager
   ```


### Conformance test

Please note that the test is not a typical unit nor integration test. Instead, it invokes the webhook in a Kubernetes-like environment which asks the webhook to send a request the DNS provider (i.e. Abion). 
The test creates a `TXT` zone record `cert-manager-dns01-tests.example.com` with a specific challenge key, verifies the presence of that record via Google DNS. Finally, it removes the entry by calling the cleanup method of the web hook.

As said above, the conformance test is run against the real [Abion API]. Therefore, you *must* have an Abion account, a domain (and zone) and an API key.

To run the conformance test you need to update abion-credentials.yaml and replace the `<ABION-API-KEY>` with a valid API Key, change the `example.com.` zone name with a valid one before you can run the test by executing: 

```
TEST_ZONE_NAME=example.com. ABION_API_HOST=https://api.abion.com ABION_API_TIMEOUT=10 make test
```

## Release
This project uses **Git tags** to drive both Docker image publishing and Helm chart releases.

### 1.Prepare Helm Chart
Before creating a release, update the chart metadata in
[`deploy/cert-manager-webhook-abion/Chart.yaml`](deploy/cert-manager-webhook-abion/Chart.yaml):

```yaml
version: 1.3.0      # match the new release version (SemVer, no leading v)
appVersion: "1.3.0" # match the new release version (SemVer, no leading v)
```
### 2. Create a Git tag
Create a new Git tag for the new release:
```bash
git add deploy/cert-manager-webhook-abion/Chart.yaml
git commit -m "Release v1.3.0"
git tag v1.3.0
git push origin main --tags
```

[ACME documentation]: https://cert-manager.io/docs/configuration/acme/
[Certificate]: https://cert-manager.io/docs/usage/certificate/
[cert-manager]: https://cert-manager.io/
[Abion]: https://abion.com/
[Abion API]: https://demo.abion.com/pmapi-doc/
[Docker Hub]: https://hub.docker.com/r/abiondevelopment/cert-manager-webhook-abion
[Helm]: https://helm.sh
[image tags]: https://hub.docker.com/r/abiondevelopment/cert-manager-webhook-abion
[minikube]: https://minikube.sigs.k8s.io/
[setting-nameservers-for-dns01-self-check]: https://cert-manager.io/docs/configuration/acme/dns01/#setting-nameservers-for-dns01-self-check