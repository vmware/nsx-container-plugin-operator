/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package configmap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/ini.v1"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/pkg/errors"
	operatortypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

var immutableFields = []struct {
	Section string
	Key     string
}{
	{
		Section: "coe",
		Key:     "cluster",
	},
	{
		Section: "nsx_v3",
		Key:     "l4_lb_auto_scaling",
	},
}

type Adaptor interface {
	FillDefaults(configmap *corev1.ConfigMap, spec *configv1.NetworkSpec) error
	validateConfigMap(configmap *corev1.ConfigMap) []error
	validateClusterNetwork(spec *configv1.NetworkSpec) []error
	HasNetworkConfigChange(currConfig *configv1.Network, prevConfig *configv1.Network) bool
	setControllerReference(r *ReconcileConfigMap, networkConfig *configv1.Network, obj metav1.Object) error
	getNetworkConfig(r *ReconcileConfigMap) (*configv1.Network, error)
}

type ConfigMap struct{}

type ConfigMapK8s struct {
	ConfigMap
}

type ConfigMapOc struct {
	ConfigMap
}

func (adaptor *ConfigMapK8s) FillDefaults(configmap *corev1.ConfigMap, spec *configv1.NetworkSpec) error {
	errs := []error{}
	data := &configmap.Data
	cfg, err := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	if err != nil {
		log.Error(err, "failed to load ConfigMap")
		return err
	}

	if cfg.Section("coe").HasKey("adaptor") && cfg.Section("coe").Key("adaptor").Value() != "kubernetes" {
		log.Info("The operator infers the adaptor to use from the environment. Any setting for coe.adaptor will be ignored")
	}
	appendErrorIfNotNil(&errs, fillDefault(cfg, "coe", "adaptor", "kubernetes", true))
	appendErrorIfNotNil(&errs, fillDefault(cfg, "nsx_v3", "policy_nsxapi", "True", true))
	appendErrorIfNotNil(&errs, fillDefault(cfg, "coe", "enable_snat", "True", false))
	appendErrorIfNotNil(&errs, fillDefault(cfg, "ha", "enable", "True", false))
	appendErrorIfNotNil(&errs, fillDefault(cfg, "nsx_node_agent", "mtu", strconv.Itoa(operatortypes.DefaultMTU), false))
	// Write config back to ConfigMap data
	(*data)[operatortypes.ConfigMapDataKey], err = iniWriteToString(cfg)
	appendErrorIfNotNil(&errs, err)

	if len(errs) > 0 {
		return errors.Errorf("failed to fill defaults: %q", errs)
	}
	return nil
}

func (adaptor *ConfigMapOc) FillDefaults(configmap *corev1.ConfigMap, spec *configv1.NetworkSpec) error {
	errs := []error{}
	data := &configmap.Data
	cfg, err := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	if err != nil {
		log.Error(err, "failed to load ConfigMap")
		return err
	}
	// We support only policy API, single tier topo on openshift4
	if cfg.Section("coe").HasKey("adaptor") && cfg.Section("coe").Key("adaptor").Value() != "openshift4" {
		log.Info("The operator infers the adaptor to use from the environment. Any setting for coe.adaptor will be ignored")
	}
	appendErrorIfNotNil(&errs, fillDefault(cfg, "coe", "adaptor", "openshift4", true))
	appendErrorIfNotNil(&errs, fillDefault(cfg, "nsx_v3", "policy_nsxapi", "True", true))
	appendErrorIfNotNil(&errs, fillDefault(cfg, "nsx_v3", "single_tier_topology", "True", true))
	appendErrorIfNotNil(&errs, fillDefault(cfg, "coe", "enable_snat", "True", false))
	appendErrorIfNotNil(&errs, fillDefault(cfg, "ha", "enable", "True", false))

	// For Openshift add a 3-seconds agent delay by default
	appendErrorIfNotNil(&errs, fillDefault(cfg, "nsx_node_agent", "waiting_before_cni_response", "3", false))
	appendErrorIfNotNil(&errs, fillDefault(cfg, "nsx_node_agent", "mtu", strconv.Itoa(operatortypes.DefaultMTU), false))
	// For Openshift force enable_ovs_mcast_snooping as False by default for IPI and UPI installation
        // keepalived is configured with unicast by default from OC 4.11
	appendErrorIfNotNil(&errs, fillDefault(cfg, "nsx_node_agent", "enable_ovs_mcast_snooping", "false", false))
	appendErrorIfNotNil(&errs, fillClusterNetwork(spec, cfg))

	// Write config back to ConfigMap data
	(*data)[operatortypes.ConfigMapDataKey], err = iniWriteToString(cfg)
	appendErrorIfNotNil(&errs, err)

	if len(errs) > 0 {
		return errors.Errorf("failed to fill defaults: %q", errs)
	}
	return nil
}

func (adaptor *ConfigMapK8s) validateConfigMap(configmap *corev1.ConfigMap) []error {
	errs := []error{}
	data := &configmap.Data
	cfg, err := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	if err != nil {
		errs = append(errs, errors.Wrapf(err, "failed to load ConfigMap"))
		return errs
	}
	appendErrorIfNotNil(&errs, validateConfig(cfg, "coe", "cluster"))
	appendErrorIfNotNil(&errs, validateConfig(cfg, "nsx_v3", "nsx_api_managers"))
	if cfg.Section("coe").Key("enable_snat").Value() == "True" {
		appendErrorIfNotNil(&errs, validateConfig(cfg, "nsx_v3", "external_ip_pools"))
	}
	appendErrorIfNotNil(&errs, validateConfig(cfg, "nsx_v3", "container_ip_blocks"))
	// Either T0 gateway or top tier router should be set
	if validateConfig(cfg, "nsx_v3", "tier0_gateway") != nil &&
		validateConfig(cfg, "nsx_v3", "top_tier_router") != nil {
		appendErrorIfNotNil(&errs, errors.Errorf("failed to get tier0_gateway or top_tier_router"))
	}

	errs = append(errs, validateNodeAgentOptions(configmap)...)
	errs = append(errs, validateCommonOptions(configmap)...)
	errs = append(errs, validateUnSupportedOptions(configmap)...)

	return errs
}

func (adaptor *ConfigMapOc) validateConfigMap(configmap *corev1.ConfigMap) []error {
	// TODO: merge validateConfigMap because most logic are the same
	errs := []error{}
	data := &configmap.Data
	cfg, err := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	if err != nil {
		errs = append(errs, errors.Wrapf(err, "failed to load ConfigMap"))
		return errs
	}
	appendErrorIfNotNil(&errs, validateConfig(cfg, "coe", "cluster"))
	appendErrorIfNotNil(&errs, validateConfig(cfg, "nsx_v3", "nsx_api_managers"))
	if cfg.Section("coe").Key("enable_snat").Value() == "True" {
		appendErrorIfNotNil(&errs, validateConfig(cfg, "nsx_v3", "external_ip_pools"))
	}
	// Either T0 gateway or top tier router should be set
	if validateConfig(cfg, "nsx_v3", "tier0_gateway") != nil &&
		validateConfig(cfg, "nsx_v3", "top_tier_router") != nil {
		appendErrorIfNotNil(&errs, errors.Errorf("failed to get tier0_gateway or top_tier_router"))
	}

	errs = append(errs, validateNodeAgentOptions(configmap)...)
	errs = append(errs, validateCommonOptions(configmap)...)
	errs = append(errs, validateUnSupportedOptions(configmap)...)

	return errs
}

func (adaptor *ConfigMapK8s) validateClusterNetwork(spec *configv1.NetworkSpec) []error {
	return []error{}
}

func (adaptor *ConfigMapOc) validateClusterNetwork(spec *configv1.NetworkSpec) []error {
	errs := []error{}
	if strings.ToLower(spec.NetworkType) != operatortypes.NetworkType {
		appendErrorIfNotNil(&errs, errors.Errorf("network type %s is not %s", spec.NetworkType, operatortypes.NetworkType))
		return errs
	}
	if len(spec.ClusterNetwork) == 0 {
		appendErrorIfNotNil(&errs, errors.Errorf("cluster network cannot be empty"))
		return errs
	}
	for idx, pool := range spec.ClusterNetwork {
		_, _, err := net.ParseCIDR(pool.CIDR)
		if err != nil {
			appendErrorIfNotNil(&errs, errors.Wrapf(err, "cluster network %d CIDR %q is invalid", idx, pool.CIDR))
			continue
		}
		cidrPrefix, err := getPrefixFromCIDR(pool.CIDR)
		if err != nil {
			appendErrorIfNotNil(&errs, errors.Wrapf(err, "unable to infer CIDR prefix in CIDR: %q", pool.CIDR))
			continue
		}

		if cidrPrefix > 30 {
			appendErrorIfNotNil(&errs, errors.Errorf("invalid CIDR prefix length for CIDR %q. it must be larger than 30", pool.CIDR))
		}

		if pool.HostPrefix < cidrPrefix {
			appendErrorIfNotNil(&errs, errors.Errorf("invalid CIDR HostPrefix: %d for CIDR %q. It must be smaller than or equal to the CIDR Prefix", pool.HostPrefix, pool.CIDR))
		}
	}
	return errs
}

func (adaptor *ConfigMapK8s) HasNetworkConfigChange(currConfig *configv1.Network, prevConfig *configv1.Network) bool {
	return false
}

func (adaptor *ConfigMapOc) HasNetworkConfigChange(currConfig *configv1.Network, prevConfig *configv1.Network) bool {
	// only detect changes in spec.ClusterNetwork and spec.ServiceNetwork
	if !stringSliceEqual(currConfig.Spec.ServiceNetwork, prevConfig.Spec.ServiceNetwork) {
		// no point to check CIDRs as well
		return true
	}
	currCidrs := []string{}
	for _, cnet := range currConfig.Spec.ClusterNetwork {
		currCidrs = append(currCidrs, cnet.CIDR)
	}
	prevCidrs := []string{}
	for _, cnet := range prevConfig.Spec.ClusterNetwork {
		prevCidrs = append(prevCidrs, cnet.CIDR)
	}
	return stringSliceEqual(currCidrs, prevCidrs)
}

func (adaptor *ConfigMapK8s) setControllerReference(r *ReconcileConfigMap, networkConfig *configv1.Network, obj metav1.Object) error {
	// Skip setControllerReference for native Kubernetes configmap controller
	return nil
}

func (adaptor *ConfigMapOc) setControllerReference(r *ReconcileConfigMap, networkConfig *configv1.Network, obj metav1.Object) error {
	err := controllerutil.SetControllerReference(networkConfig, obj, r.scheme)
	return err
}

func (adaptor *ConfigMapK8s) getNetworkConfig(r *ReconcileConfigMap) (*configv1.Network, error) {
	return &configv1.Network{}, nil
}

func (adaptor *ConfigMapOc) getNetworkConfig(r *ReconcileConfigMap) (*configv1.Network, error) {
	networkConfig := &configv1.Network{}
	err := r.client.Get(context.TODO(), types.NamespacedName{Name: operatortypes.NetworkCRDName}, networkConfig)
	return networkConfig, err
}

func appendErrorIfNotNil(errs *[]error, err error) {
	if err == nil {
		return
	}
	*errs = append(*errs, err)
}

func fillDefault(cfg *ini.File, sec string, key string, val string, force bool) error {
	_, err := cfg.GetSection(sec)
	if err != nil {
		return errors.Wrapf(err, "failed to get section %s", sec)
	}
	if !cfg.Section(sec).HasKey(key) {
		_, err = cfg.Section(sec).NewKey(key, val)
		if err != nil {
			return errors.Wrapf(err, "failed to fill key %s default value %s in section %s", key, val, sec)
		}
	} else if cfg.Section(sec).Key(key).Value() == "" || force == true {
		cfg.Section(sec).Key(key).SetValue(val)
	}
	return nil
}

func fillClusterNetwork(spec *configv1.NetworkSpec, cfg *ini.File) error {
	ipBlocks := []string{}
	for _, block := range spec.ClusterNetwork {
		ipBlocks = append(ipBlocks, block.CIDR)
	}
	return fillDefault(cfg, "nsx_v3", "container_ip_blocks", strings.Join(ipBlocks[:], ","), true)
}

func validateConfig(cfg *ini.File, sec string, key string) error {
	_, err := cfg.GetSection(sec)
	if err != nil {
		return errors.Wrapf(err, "failed to get section %s", sec)
	}
	if !cfg.Section(sec).HasKey(key) || cfg.Section(sec).Key(key).Value() == "" {
		return errors.Errorf("failed to get key %s from section %s", key, sec)
	}
	return nil
}

// Validate node agent specific options
func validateNodeAgentOptions(configmap *corev1.ConfigMap) []error {
	errs := []error{}
	data := &configmap.Data
	cfg, err := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))

	// Check MTU value
	mtu := cfg.Section("nsx_node_agent").Key("mtu").Value()
	_, err = strconv.Atoi(mtu)
	if err != nil {
		appendErrorIfNotNil(&errs, errors.Wrapf(err, "mtu is invalid"))
	}
	return errs
}

// Validate common options for both ncp and node agent
func validateCommonOptions(configmap *corev1.ConfigMap) []error {
	errs := []error{}
	data := &configmap.Data
	cfg, _ := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	// Check DEFAULT section log_file option
	if cfg.Section("DEFAULT").Key("log_file").Value() != "" {
		appendErrorIfNotNil(&errs, errors.Errorf("DEFAULT section option: log_file is not allowed to change, current value:%s",
			cfg.Section("DEFAULT").Key("log_file").Value()))
	}
	return errs
}

// Validate non supported options
// Including WCP and TAS specific options, MP supported options
func validateUnSupportedOptions(configmap *corev1.ConfigMap) []error {
	errs := []error{}
	data := &configmap.Data
	is_changed := false
	cfg, err := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))

	for section, keys := range operatortypes.MPOptions {
		for _, key := range keys {
			if optionInConfigMap(configmap, section, key) {
				log.Info(fmt.Sprintf("%s section option: %s is not supported in PolicyAPI, ignored this config option", section, key))
				cfg.Section(section).DeleteKey(key)
				is_changed = true
			}
		}
	}

	for section, keys := range operatortypes.WCPOptions {
		for _, key := range keys {
			if optionInConfigMap(configmap, section, key) {
				log.Info(fmt.Sprintf("%s section option: %s is not supported in Operator, ignored this config option", section, key))
				cfg.Section(section).DeleteKey(key)
				is_changed = true
			}
		}
	}

	if len(cfg.Section(operatortypes.TASSection).Keys()) != 0 {
		log.Info(fmt.Sprintf("%s section options are not supported in Operator, ignored this section", operatortypes.TASSection))
		cfg.DeleteSection(operatortypes.TASSection)
		is_changed = true
	}

	if is_changed {
		// Write config back to ConfigMap data
		(*data)[operatortypes.ConfigMapDataKey], err = iniWriteToString(cfg)
		appendErrorIfNotNil(&errs, err)
	}
	return errs
}

func getPrefixFromCIDR(cidr string) (uint32, error) {
	cidrPrefix := cidr[strings.LastIndex(cidr, "/")+1:]
	i, err := strconv.ParseUint(cidrPrefix, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(i), nil
}

func Render(configmap *corev1.ConfigMap, ncpReplicas *int32, ncpNodeSelector *map[string]string,
	ncpTolerations *[]corev1.Toleration, nsxNodeAgentDsTolerations *[]corev1.Toleration,
	nsxSecret *corev1.Secret, lbSecret *corev1.Secret,
) ([]*unstructured.Unstructured, error) {
	log.Info("Starting render phase")
	objs := []*unstructured.Unstructured{}

	// Set configmap data
	data := configmap.Data
	cfg, err := ini.Load([]byte(data[operatortypes.ConfigMapDataKey]))
	if err != nil {
		return nil, errors.Wrap(err, "failed to load ConfigMap")
	}
	renderData := render.MakeRenderData()
	renderData.Data[operatortypes.NcpConfigMapRenderKey], err = generateConfigMap(cfg, operatortypes.NcpSections)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render nsx-ncp ConfigMap")
	}
	renderData.Data[operatortypes.NodeAgentConfigMapRenderKey], err = generateConfigMap(cfg, operatortypes.AgentSections)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render nsx-node-agent ConfigMap")
	}

	// Set NCP image
	ncpImage := os.Getenv(operatortypes.NcpImageEnv)
	if ncpImage == "" {
		ncpImage = "nsx-ncp:latest"
	}
	renderData.Data[operatortypes.NcpImageKey] = ncpImage

	// Set NCP replicas
	haEnabled := false
	if cfg.Section("ha").HasKey("enable") {
		haEnabled, err = strconv.ParseBool(cfg.Section("ha").Key("enable").Value())
		if err != nil {
			return nil, errors.Wrap(err, "failed to get ha option")
		}
	}

	if !haEnabled && *ncpReplicas != 1 {
		log.Info(fmt.Sprintf("Set nsx-ncp deployment replicas to 1 instead of %d as HA is deactivated",
			*ncpReplicas))
		*ncpReplicas = 1
	}
	renderData.Data[operatortypes.NcpReplicasKey] = *ncpReplicas

	// Set NCP NodeSelector
	if ncpNodeSelector != nil {
		jsonStr, err := json.Marshal(ncpNodeSelector)
		if err != nil {
			return nil, errors.Wrap(err, "failed to get NCP NodeSelector")
		}
		// Convert ncpNodeSelector to string
		renderData.Data[operatortypes.NcpNodeSelectorRenderKey] = string(jsonStr)
	} else {
		renderData.Data[operatortypes.NcpNodeSelectorRenderKey] = ""
	}

	// Set NCP Tolerations
	if ncpTolerations != nil {
		jsonStr, err := json.Marshal(ncpTolerations)
		if err != nil {
			return nil, errors.Wrap(err, "failed to get NCP Tolerations")
		}
		// Convert ncpTolerations to string
		renderData.Data[operatortypes.NcpTolerationsRenderKey] = string(jsonStr)
	} else {
		renderData.Data[operatortypes.NcpTolerationsRenderKey] = ""
	}

	// Set Nsx Node Agent Ds Tolerations
	if nsxNodeAgentDsTolerations != nil {
		jsonStr, err := json.Marshal(nsxNodeAgentDsTolerations)
		if err != nil {
			return nil, errors.Wrap(err, "failed to get Nsx Node Agent Tolerations")
		}
		// Convert nsxNodeAgentDsTolerations to string
		renderData.Data[operatortypes.NsxNodeTolerationsRenderKey] = string(jsonStr)
	} else {
		renderData.Data[operatortypes.NsxNodeTolerationsRenderKey] = ""
	}

	// Set LB secret
	if lbSecret != nil {
		renderData.Data[operatortypes.LbCertRenderKey] = base64.StdEncoding.EncodeToString(lbSecret.Data["tls.crt"])
		renderData.Data[operatortypes.LbKeyRenderKey] = base64.StdEncoding.EncodeToString(lbSecret.Data["tls.key"])
	} else {
		renderData.Data[operatortypes.LbCertRenderKey] = ""
		renderData.Data[operatortypes.LbKeyRenderKey] = ""
	}
	// Set NSX secret
	if nsxSecret != nil {
		renderData.Data[operatortypes.NsxCertRenderKey] = base64.StdEncoding.EncodeToString(nsxSecret.Data["tls.crt"])
		renderData.Data[operatortypes.NsxKeyRenderKey] = base64.StdEncoding.EncodeToString(nsxSecret.Data["tls.key"])
		// Render tls.ca only if specified
		if tls_ca, found := nsxSecret.Data["tls.ca"]; found {
			renderData.Data[operatortypes.NsxCARenderKey] = base64.StdEncoding.EncodeToString(tls_ca)
		} else {
			renderData.Data[operatortypes.NsxCARenderKey] = ""
		}
	} else {
		renderData.Data[operatortypes.NsxCertRenderKey] = ""
		renderData.Data[operatortypes.NsxKeyRenderKey] = ""
		renderData.Data[operatortypes.NsxCARenderKey] = ""
	}
	// Set value of use_nsx_ovs_kernel_module
	if cfg.Section("nsx_node_agent").Key("use_nsx_ovs_kernel_module").Value() == "True" {
		renderData.Data[operatortypes.NsxOvsKmodRenderKey] = true
	} else {
		renderData.Data[operatortypes.NsxOvsKmodRenderKey] = false
	}
	manifestDir, err := GetManifestDir()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get manifestDir")
	}
	log.Info(fmt.Sprintf("Got the manifest dir: %s", manifestDir))
	manifests, err := render.RenderDir(manifestDir, &renderData)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render manifests")
	}

	objs = append(objs, manifests...)
	return objs, nil
}

func GetManifestDir() (string, error) {
	release, err := os.Open(operatortypes.OsReleaseFile)
	if err != nil {
		return "", err
	}
	defer release.Close()
	content, err := ioutil.ReadAll(release)
	osRelease := string(content)
	if strings.Contains(osRelease, "CoreOS") {
		// TODO: add platform detection logic if we allow the user deploy coreos image on native k8s
		return "./manifest/openshift4/coreos", nil
	} else if strings.Contains(osRelease, "Ubuntu") {
		return "./manifest/kubernetes/ubuntu", nil
	} else if strings.Contains(osRelease, "CentOS") || strings.Contains(osRelease, "Red Hat") {
		return "./manifest/kubernetes/rhel", nil
	}
	return "", errors.Wrap(err, "failed to get os-release")
}

type podNeedChange struct {
	ncp       bool
	agent     bool
	bootstrap bool
}

func NeedApplyChange(currConfig *corev1.ConfigMap, prevConfig *corev1.ConfigMap) (
	podNeedChange, error,
) {
	if prevConfig == nil {
		return podNeedChange{true, true, true}, nil
	}

	currData := currConfig.Data
	prevData := prevConfig.Data
	// Compare the whole data
	if strings.Compare(strings.TrimSpace(currData[operatortypes.ConfigMapDataKey]),
		strings.TrimSpace(prevData[operatortypes.ConfigMapDataKey])) == 0 {
		return podNeedChange{false, false, false}, nil
	}
	// Compare every section to get different section slice
	currCfg, err := ini.Load([]byte(currData[operatortypes.ConfigMapDataKey]))
	if err != nil {
		log.Error(err, "Failed to load new ConfigMap")
		return podNeedChange{false, false, false}, err
	}
	prevCfg, err := ini.Load([]byte(prevData[operatortypes.ConfigMapDataKey]))
	if err != nil {
		log.Error(err, "Failed to load previous ConfigMap")
		return podNeedChange{false, false, false}, err
	}

	// Check whether immutable fields are changed. This check should return an error to
	// make sure that reconcile could be terminated.
	if immutableFieldChanged(currCfg, prevCfg) {
		return podNeedChange{}, errors.New("Immutable field can't be changed.")
	}

	diffSecs := []string{}
	currSecs := currCfg.SectionStrings()
	for _, name := range currSecs {
		if !hasSection(prevCfg, name) {
			if len(currCfg.Section(name).KeyStrings()) != 0 {
				log.Info(fmt.Sprintf("Section [%s] is added into configmap", name))
				diffSecs = append(diffSecs, name)
			}
			continue
		}
		if !reflect.DeepEqual(currCfg.Section(name).KeysHash(), prevCfg.Section(name).KeysHash()) {
			// Check which configmap option value is changed
			keys := currCfg.Section(name).KeyStrings()
			for _, key := range keys {
				curKeyValue := currCfg.Section(name).Key(key).Value()
				// Need to hide value for user-sensitive information option
				keySensitive := false
				if name == "nsx_v3" && (key == "lb_default_cert_path" || key == "lb_priv_key_path" ||
					key == "nsx_api_cert_file" || key == "nsx_api_private_key_file" || key == "nsx_api_password") {
					keySensitive = true
				}
				if prevCfg.Section(name).HasKey(key) {
					preKeyValue := prevCfg.Section(name).Key(key).Value()
					if !reflect.DeepEqual(preKeyValue, curKeyValue) {
						if keySensitive {
							log.Info(fmt.Sprintf("Section [%s] config option: %s is changed", name, key))
						} else {
							log.Info(fmt.Sprintf("Section [%s] config option: %s = %s is changed to %s = %s",
								name, key, preKeyValue, key, curKeyValue))
						}
					}
				} else {
					if keySensitive {
						log.Info(fmt.Sprintf("Section [%s] add/uncomment a new config option: %s",
							name, key))
					} else {
						log.Info(fmt.Sprintf("Section [%s] add/uncomment a new config option: %s = %s",
							name, key, curKeyValue))
					}
				}
			}
			keys = prevCfg.Section(name).KeyStrings()
			for _, key := range keys {
				if !currCfg.Section(name).HasKey(key) {
					log.Info(fmt.Sprintf("Section [%s] remove/comment a config option: %s", name, key))
				}
			}

			diffSecs = append(diffSecs, name)
		}
	}
	prevSecs := prevCfg.SectionStrings()
	for _, name := range prevSecs {
		if !hasSection(currCfg, name) {
			if len(prevCfg.Section(name).KeyStrings()) != 0 {
				log.Info(fmt.Sprintf("Section [%s] is removed from configmap", name))
				diffSecs = append(diffSecs, name)
			}
		}
	}
	// Check whether different sections impact on ncp, nsx-node-agent and nsx-ncp-bootstrap
	needChange := podNeedChange{}
	for _, sec := range diffSecs {
		if !needChange.ncp && inSlice(sec, operatortypes.NcpSections) {
			needChange.ncp = true
		}
		if !needChange.agent && inSlice(sec, operatortypes.AgentRestartSections) {
			needChange.agent = true
		}
		if !needChange.agent && checkOptionsChange(operatortypes.AgentRestartOptionKeys, sec, prevCfg, currCfg) {
			needChange.agent = true
		}
		if !needChange.bootstrap && checkOptionsChange(operatortypes.BootstrapRestartOptionKeys, sec, prevCfg, currCfg) {
			needChange.bootstrap = true
		}
		if needChange.ncp && needChange.agent && needChange.bootstrap {
			break
		}
	}

	if len(diffSecs) > 0 {
		log.Info(fmt.Sprintf("Section %s changed", diffSecs))
	}

	return needChange, nil
}

func checkOptionsChange(options map[string][]string, sec string, prevCfg, currCfg *ini.File) bool {
	if _, ok := options[sec]; !ok {
		return false
	}
	if hasSection(prevCfg, sec) != hasSection(currCfg, sec) {
		return true
	}
	// We can assert that section `sec` exists in both prevCfg and currCfg here.
	for _, key := range options[sec] {
		if prevCfg.Section(sec).HasKey(key) != currCfg.Section(sec).HasKey(key) {
			return true
		}
		if prevCfg.Section(sec).HasKey(key) && currCfg.Section(sec).HasKey(key) &&
			prevCfg.Section(sec).Key(key).Value() != currCfg.Section(sec).Key(key).Value() {
			return true
		}
	}
	return false
}

func immutableFieldChanged(cur, prev *ini.File) bool {
	for _, field := range immutableFields {
		if cur.Section(field.Section).Key(field.Key).String() != prev.Section(field.Section).Key(field.Key).String() {
			log.Info(fmt.Sprintf("Immutable field %s.%s shouldn't be changed, skip applying changes.", field.Section, field.Key))
			return true
		}
	}
	return false
}

func inSlice(str string, s []string) bool {
	for _, v := range s {
		if str == v {
			return true
		}
	}
	return false
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, v := range a {
		if !inSlice(v, b) {
			return false
		}
	}
	return true
}

func generateConfigMap(srcCfg *ini.File, sections []string) (string, error) {
	destCfg := ini.Empty()
	for _, name := range sections {
		srcSec, err := srcCfg.GetSection(name)
		if err != nil {
			continue
		}
		destCfg.NewSection(name)
		keys := srcSec.KeyStrings()
		for _, key := range keys {
			destCfg.Section(name).NewKey(key, srcSec.Key(key).Value())
		}
	}

	return iniWriteToString(destCfg)
}

func GenerateOperatorConfigMap(opConfigmap *corev1.ConfigMap, ncpConfigMap *corev1.ConfigMap,
	agentConfigMap *corev1.ConfigMap,
) error {
	ncpCfg, err := ini.Load([]byte(ncpConfigMap.Data[operatortypes.ConfigMapDataKey]))
	if err != nil {
		log.Error(err, "Failed to load nsx-ncp ConfigMap")
		return err
	}
	agentCfg, err := ini.Load([]byte(agentConfigMap.Data[operatortypes.ConfigMapDataKey]))
	if err != nil {
		log.Error(err, "Failed to load nsx-node-agent ConfigMap")
		return err
	}

	opCfg := ini.Empty()
	for _, name := range operatortypes.OperatorSections {
		sec, err := ncpCfg.GetSection(name)
		if err != nil {
			sec, err = agentCfg.GetSection(name)
			if err != nil {
				continue
			}
		}
		opCfg.NewSection(name)
		keys := sec.KeyStrings()
		for _, key := range keys {
			opCfg.Section(name).NewKey(key, sec.Key(key).Value())
		}
	}

	opConfigmap.Data[operatortypes.ConfigMapDataKey], err = iniWriteToString(opCfg)
	if err != nil {
		log.Error(err, "Failed to generate operator ConfigMap")
		return err
	}
	return nil
}

func hasSection(cfg *ini.File, section string) bool {
	_, err := cfg.GetSection(section)
	return err == nil
}

func iniWriteToString(cfg *ini.File) (string, error) {
	var buf bytes.Buffer
	_, err := cfg.WriteTo(&buf)
	if err != nil {
		return "", err
	}
	// go-ini does not write DEFAULT section name to buffer, so add it here
	cfgString := "\n[DEFAULT]\n" + buf.String()
	return cfgString, nil
}

func optionInConfigMap(configMap *corev1.ConfigMap, section string, key string) bool {
	cfg, err := ini.Load([]byte(configMap.Data[operatortypes.ConfigMapDataKey]))
	if err != nil {
		log.Error(err, "Failed to load ConfigMap")
		return false
	}
	sec, err := cfg.GetSection(section)
	if err != nil {
		return false
	}
	if sec.HasKey(key) && sec.Key(key).Value() != "" {
		return true
	}
	return false
}

func getOptionInConfigMap(configMap *corev1.ConfigMap, section string, key string) string {
	if configMap == nil {
		return ""
	}
	cfg, err := ini.Load([]byte(configMap.Data[operatortypes.ConfigMapDataKey]))
	if err != nil {
		log.Error(err, "Failed to load ConfigMap")
		return ""
	}
	sec, err := cfg.GetSection(section)
	if err != nil {
		log.Info(fmt.Sprintf("Section %s not found", section))
		return ""
	}
	if sec.HasKey(key) {
		return sec.Key(key).Value()
	}
	return ""
}

func IsMTUChanged(currConfigMap *corev1.ConfigMap, prevConfigMap *corev1.ConfigMap) bool {
	currMtu := getOptionInConfigMap(currConfigMap, "nsx_node_agent", "mtu")
	prevMtu := getOptionInConfigMap(prevConfigMap, "nsx_node_agent", "mtu")
	if currMtu == prevMtu {
		return false
	} else {
		return true
	}
}

func FillNsxAuthCfg(configmap *corev1.ConfigMap, nsxSecret *corev1.Secret) error {
	errs := []error{}
	data := &configmap.Data
	cfg, err := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	if err != nil {
		log.Error(err, "failed to load ConfigMap")
		return err
	}

	if nsx_cert, found := nsxSecret.Data["tls.crt"]; found && len(nsx_cert) > 0 {
		appendErrorIfNotNil(&errs, fillDefault(cfg, "nsx_v3", "nsx_api_cert_file", "/etc/nsx-ujo/nsx-cert/tls.crt", true))
	}

	if nsx_key, found := nsxSecret.Data["tls.key"]; found && len(nsx_key) > 0 {
		appendErrorIfNotNil(&errs, fillDefault(cfg, "nsx_v3", "nsx_api_private_key_file", "/etc/nsx-ujo/nsx-cert/tls.key", true))
	}

	if ca_file, found := nsxSecret.Data["tls.ca"]; found && len(ca_file) > 0 {
		appendErrorIfNotNil(&errs, fillDefault(cfg, "nsx_v3", "ca_file", "/etc/nsx-ujo/nsx-cert/tls.ca", true))
	}

	// Write config back to ConfigMap data
	(*data)[operatortypes.ConfigMapDataKey], err = iniWriteToString(cfg)
	appendErrorIfNotNil(&errs, err)

	if len(errs) > 0 {
		return errors.Errorf("failed to fill nsx authentication configuration: %q", errs)
	}
	return nil
}

func FillLbCertCfg(configmap *corev1.ConfigMap, lbSecret *corev1.Secret) error {
	errs := []error{}
	data := &configmap.Data
	cfg, err := ini.Load([]byte((*data)[operatortypes.ConfigMapDataKey]))
	if err != nil {
		log.Error(err, "failed to load ConfigMap")
		return err
	}

	if lb_cert, found := lbSecret.Data["tls.crt"]; found && len(lb_cert) > 0 {
		appendErrorIfNotNil(&errs, fillDefault(cfg, "nsx_v3", "lb_default_cert_path", "/etc/nsx-ujo/lb-cert/tls.crt", true))
	}

	if lb_key, found := lbSecret.Data["tls.key"]; found && len(lb_key) > 0 {
		appendErrorIfNotNil(&errs, fillDefault(cfg, "nsx_v3", "lb_priv_key_path", "/etc/nsx-ujo/lb-cert/tls.key", true))
	}

	// Write config back to ConfigMap data
	(*data)[operatortypes.ConfigMapDataKey], err = iniWriteToString(cfg)
	appendErrorIfNotNil(&errs, err)

	if len(errs) > 0 {
		return errors.Errorf("failed to fill lb certification configuration: %q", errs)
	}
	return nil
}
