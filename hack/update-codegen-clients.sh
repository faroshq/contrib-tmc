#!/usr/bin/env bash

# Copyright 2021 The KCP Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail
set -o xtrace

export GOPATH=$(go env GOPATH)

SCRIPT_ROOT=$(dirname "${BASH_SOURCE[0]}")/..
pushd "${SCRIPT_ROOT}"
BOILERPLATE_HEADER="$( pwd )/hack/boilerplate/boilerplate.go.txt"
popd
CODEGEN_PKG=${CODEGEN_PKG:-$(cd "${SCRIPT_ROOT}"; go list -f '{{.Dir}}' -m k8s.io/code-generator)}

# TODO: use generate-groups.sh directly instead once https://github.com/kubernetes/kubernetes/pull/114987 is available
go install "${CODEGEN_PKG}"/cmd/applyconfiguration-gen
go install "${CODEGEN_PKG}"/cmd/client-gen

"$GOPATH"/bin/applyconfiguration-gen \
  --input-dirs github.com/faroshq/tmc/apis/scheduling/v1alpha1 \
  --input-dirs github.com/faroshq/tmc/apis/workload/v1alpha1 \
  --input-dirs github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1 \
  --input-dirs github.com/kcp-dev/kcp/sdk/apis/apiresource/v1alpha1 \
  --input-dirs github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1 \
  --input-dirs github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1 \
  --input-dirs k8s.io/apimachinery/pkg/apis/meta/v1,k8s.io/apimachinery/pkg/runtime,k8s.io/apimachinery/pkg/version,k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1 \
  --output-package github.com/faroshq/tmc/client/applyconfiguration \
  --go-header-file ./hack/../hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/faroshq/tmc

"$GOPATH"/bin/client-gen \
  --input github.com/faroshq/tmc/apis/scheduling/v1alpha1 \
  --input github.com/faroshq/tmc/apis/workload/v1alpha1 \
  --input-base="" \
  --apply-configuration-package=github.com/faroshq/tmc/client/applyconfiguration \
  --clientset-name "versioned"  \
  --output-package github.com/faroshq/tmc/client/clientset \
  --go-header-file ./hack/../hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/faroshq/tmc

bash "${CODEGEN_PKG}"/generate-groups.sh "deepcopy" \
  github.com/faroshq/tmc/client github.com/faroshq/tmc/apis \
  "workload:v1alpha1 scheduling:v1alpha1" \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/faroshq/tmc

pushd ./apis
${CODE_GENERATOR} \
  "client:outputPackagePath=github.com/faroshq/tmc/client,apiPackagePath=github.com/faroshq/tmc/apis,singleClusterClientPackagePath=github.com/faroshq/tmc/client/clientset/versioned,singleClusterApplyConfigurationsPackagePath=github.com/faroshq/tmc/client/applyconfiguration,headerFile=${BOILERPLATE_HEADER}" \
  "lister:apiPackagePath=github.com/faroshq/tmc/apis,headerFile=${BOILERPLATE_HEADER}" \
  "informer:outputPackagePath=github.com/faroshq/tmc/client,singleClusterClientPackagePath=github.com/faroshq/tmc/client/clientset/versioned,apiPackagePath=github.com/faroshq/tmc/apis,headerFile=${BOILERPLATE_HEADER}" \
  "paths=./..." \
  "output:dir=./../client"
popd

bash "${CODEGEN_PKG}"/generate-groups.sh "deepcopy" \
  github.com/kcp-dev/kcp/third_party/conditions/client github.com/kcp-dev/kcp/third_party/conditions/apis \
  "conditions:v1alpha1" \
  --go-header-file "${SCRIPT_ROOT}"/hack/boilerplate/boilerplate.generatego.txt \
  --output-base "${SCRIPT_ROOT}" \
  --trim-path-prefix github.com/faroshq/tmc

go install "${CODEGEN_PKG}"/cmd/openapi-gen

"$GOPATH"/bin/openapi-gen  \
--input-dirs github.com/faroshq/tmc/apis/workload/v1alpha1 \
--input-dirs github.com/faroshq/tmc/apis/scheduling/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/sdk/apis/apiresource/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1 \
--input-dirs github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1 \
--input-dirs k8s.io/apimachinery/pkg/apis/meta/v1,k8s.io/apimachinery/pkg/runtime,k8s.io/apimachinery/pkg/version \
--output-package github.com/faroshq/tmc/openapi -O zz_generated.openapi \
--go-header-file ./hack/../hack/boilerplate/boilerplate.generatego.txt \
--output-base "${SCRIPT_ROOT}" \
--trim-path-prefix github.com/faroshq/tmc
