#!/bin/bash

set -e
while [[ $# -gt 0 ]]
do
key="$1"
case $key in
    --bundle-repo)
    BUNDLE_REPO="$2"
    shift 2
    ;;
    --bundle-image)
    BUNDLE_IMG_NAME="$2"
    shift 2
    ;;
    --bundle-version)
    BUNDLE_VERSION="$2"
    shift 2
    ;;
    *)    # unknown option
    echo "Unknown option $1."
    exit 1
    ;;
esac
done

if [ -z "${BUNDLE_REPO}" ]; then
  echo "Bundle repo must be specified."
  exit 1
fi

if [ -z "${BUNDLE_IMG_NAME}" ]; then
  echo "Bundle image must be specified."
  exit 1
fi

if [ -z "${BUNDLE_VERSION}" ]; then
  echo "Bundle version must be specified."
  exit 1
fi

# copy kubernetes manifests and update csv with faq
pushd ./deploy/kubernetes
cp configmap.yaml ../../bundle/kubernetes/manifests
cp lb-secret.yaml ../../bundle/kubernetes/manifests
cp nsx-secret.yaml ../../bundle/kubernetes/manifests
cp operator.nsx.vmware.com_ncpinstalls_crd.yaml ../../bundle/kubernetes/manifests
faq -f yaml -o yaml --slurp \
    '.[0].spec.install = {strategy: "deployment", spec:{ deployments: [{name: .[1].metadata.name, template: .[1].spec }], permissions: [{serviceAccountName: .[3].metadata.name, rules: .[2].rules }]}} | .[0]' \
    ../../bundle/kubernetes/manifests/nsx-container-plugin-operator.clusterserviceversion.yaml operator.yaml role.yaml service_account.yaml > csv.tmp.yaml && cat csv.tmp.yaml > \
    ../../bundle/kubernetes/manifests/nsx-container-plugin-operator.clusterserviceversion.yaml && rm csv.tmp.yaml
popd

# copy openshift4 manifests and update csv with faq
pushd ./deploy/openshift4
cp configmap.yaml ../../bundle/openshift4/manifests
cp lb-secret.yaml ../../bundle/openshift4/manifests
cp nsx-secret.yaml ../../bundle/openshift4/manifests
cp operator.nsx.vmware.com_ncpinstalls_crd.yaml ../../bundle/openshift4/manifests
faq -f yaml -o yaml --slurp \
    '.[0].spec.install = {strategy: "deployment", spec:{ deployments: [{name: .[1].metadata.name, template: .[1].spec }], permissions: [{serviceAccountName: .[3].metadata.name, rules: .[2].rules }]}} | .[0]' \
    ../../bundle/openshift4/manifests/nsx-container-plugin-operator.clusterserviceversion.yaml operator.yaml role.yaml service_account.yaml > csv.tmp.yaml && cat csv.tmp.yaml > \
    ../../bundle/openshift4/manifests/nsx-container-plugin-operator.clusterserviceversion.yaml && rm csv.tmp.yaml
popd

pushd ./bundle
# build kubernetes bundle image
docker build -t ${BUNDLE_REPO}/${BUNDLE_IMG_NAME}-k8s:${BUNDLE_VERSION} -f ./kubernetes/bundle.Dockerfile ./kubernetes
docker push ${BUNDLE_REPO}/${BUNDLE_IMG_NAME}-k8s:${BUNDLE_VERSION}
opm index add --build-tool docker --bundles ${BUNDLE_REPO}/${BUNDLE_IMG_NAME}-k8s:${BUNDLE_VERSION} --tag ${BUNDLE_REPO}/ncp-operator-index-k8s:${BUNDLE_VERSION}

# build openshift4 bundle image
docker build -t ${BUNDLE_REPO}/${BUNDLE_IMG_NAME}-oc:${BUNDLE_VERSION} -f ./openshift4/bundle.Dockerfile ./openshift4
docker push ${BUNDLE_REPO}/${BUNDLE_IMG_NAME}-oc:${BUNDLE_VERSION}
opm index add --build-tool docker --bundles ${BUNDLE_REPO}/${BUNDLE_IMG_NAME}-oc:${BUNDLE_VERSION} --tag ${BUNDLE_REPO}/ncp-operator-index-oc:${BUNDLE_VERSION}
popd