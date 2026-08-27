# Deploying Frappe SNE with Mounted CRM on KinD (Kubernetes 1.36)

This setup runs a Frappe Single Node Environment (SNE) in a Kubernetes 1.36 KinD cluster with the Frappe CRM app mounted from an OCI package (`crm-1.82.0.fpm`), serving the complete backend and compiled Vue 3 / Vite frontend without rebuilding the base image.

---

## Files

1. [`kind-config.yaml`](file:///Users/varkrish/personal/1frappe_ecosystem/fpm/deploy/k8s-sne-crm/kind-config.yaml):
   - Configures the KinD cluster with the `ImageVolume=true` feature gate enabled.
   - Forwards NodePort `30080` to host `127.0.0.1:8088`.

2. [`frappe-sne-crm.yaml`](file:///Users/varkrish/personal/1frappe_ecosystem/fpm/deploy/k8s-sne-crm/frappe-sne-crm.yaml):
   - **Init Container**: Extracts `crm-1.82.0.fpm` into shared volumes for the app code and frontend assets.
   - **Main Container**: Runs `docker.io/vyogo/erpnext:sne-version-16` with the CRM app mounted at `/home/frappe/frappe-bench/apps/crm` and assets at `/home/frappe/frappe-bench/sites/assets/crm`.
   - **Service**: Exposes port `8000` via NodePort `30080`.

---

## Deployment Steps

### 1. Create the KinD Cluster
```bash
KIND_EXPERIMENTAL_PROVIDER=podman kind create cluster \
  --name frappe-kind \
  --config deploy/k8s-sne-crm/kind-config.yaml \
  --image kindest/node:v1.36.4
```

### 2. Import SNE Base Image into the Node
```bash
podman save docker.io/vyogo/erpnext:sne-version-16 | \
  podman exec -i frappe-kind-control-plane ctr -n k8s.io images import -
```

### 3. Stage the CRM FPM Package on the Node
```bash
# Pull the OCI artifact layer from GHCR
skopeo copy docker://ghcr.io/vyogotech/fpm/frappe/crm:48cb3cccec12ef798f8faacc165fff3dc008a8fa dir:/tmp/crm-layer
# Copy the layer blob to the node
podman cp /tmp/crm-layer/<blob_sha> frappe-kind-control-plane:/var/tmp/crm-1.82.0.fpm
```

### 4. Deploy the Pod & Service
```bash
kubectl apply -f deploy/k8s-sne-crm/frappe-sne-crm.yaml
```

### 5. Install the App on the Site & Set Password
```bash
# Install CRM on the site
kubectl exec frappe-sne-crm -c frappe-sne -- bash -c \
  "cd /home/frappe/frappe-bench/sites && ../env/bin/python -m frappe.utils.bench_helper frappe --site dev.localhost install-app crm"

# Set Administrator password
kubectl exec frappe-sne-crm -c frappe-sne -- bash -c \
  "cd /home/frappe/frappe-bench/sites && ../env/bin/python -m frappe.utils.bench_helper frappe --site dev.localhost set-admin-password admin"

# Flush cache
kubectl exec frappe-sne-crm -c frappe-sne -- bash -c \
  "redis-cli flushall && cd /home/frappe/frappe-bench/sites && ../env/bin/python -m frappe.utils.bench_helper frappe --site dev.localhost clear-cache"
```

---

## Access the Running UI
- **URL**: [http://127.0.0.1:8088/login](http://127.0.0.1:8088/login)
- **CRM App**: [http://127.0.0.1:8088/crm](http://127.0.0.1:8088/crm)
- **Credentials**: `Administrator` / `admin`
