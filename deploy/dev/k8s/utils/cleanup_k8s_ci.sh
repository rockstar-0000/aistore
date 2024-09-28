# Kill all pods and services created by ci job and ignore errors
kubectl delete pods -l app=ais,type=ais-target || true
kubectl delete pods -l app=ais,type=ais-proxy || true
kubectl delete svc -l app=ais || true

# Use a cleanup job to delete any AIS files mounted with hostpath inside the minikube vm
export PARENT_DIR="/tmp"
export HOST_PATH="/tmp/ais"
export JOB_NAME="test-cleanup"
envsubst < kube_templates/cleanup_job_template.yml > cleanup_job.yml
kubectl apply -f cleanup_job.yml