/* Copyright © 2020 VMware, Inc. All Rights Reserved.
   SPDX-License-Identifier: Apache-2.0 */

package configmap

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/pkg/errors"
	ncptypes "github.com/vmware/nsx-container-plugin-operator/pkg/types"
	"gopkg.in/ini.v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/kubectl/pkg/scheme"
)

func init() {
	configv1.AddToScheme(scheme.Scheme)
}

func createMockConfigMap() *corev1.ConfigMap {
	mockConfigMap := &corev1.ConfigMap{Data: map[string]string{}}
	data := &mockConfigMap.Data
	cfg, _ := ini.Load([]byte((*data)[ncptypes.ConfigMapDataKey]))
	cfg.NewSections(ncptypes.OperatorSections...)
	(*data)[ncptypes.ConfigMapDataKey], _ = iniWriteToString(cfg)

	return mockConfigMap
}

func createMockNetworkSpec(cidrs []string) *configv1.NetworkSpec {
	mockSpec := &configv1.NetworkSpec{ClusterNetwork: []configv1.ClusterNetworkEntry{}}
	for _, cidr := range cidrs {
		entry := configv1.ClusterNetworkEntry{CIDR: cidr}
		mockSpec.ClusterNetwork = append(mockSpec.ClusterNetwork, entry)
	}
	return mockSpec
}

func TestFillDefaults(t *testing.T) {
	cidrs := []string{"10.0.0.0/24"}
	mockConfigMap := createMockConfigMap()
	mockNetworkSpec := createMockNetworkSpec(cidrs)
	data := &mockConfigMap.Data

	err := FillDefaults(mockConfigMap, mockNetworkSpec)
	if err != nil {
		t.Fatalf("failed to fill default config")
	}
	cfg, err := ini.Load([]byte((*data)[ncptypes.ConfigMapDataKey]))
	assert.Equal(t, "openshift4", cfg.Section("coe").Key("adaptor").Value())
	assert.Equal(t, "True", cfg.Section("nsx_v3").Key("policy_nsxapi").Value())
	assert.Equal(t, "True", cfg.Section("nsx_v3").Key("single_tier_topology").Value())
	assert.Equal(t, "True", cfg.Section("coe").Key("enable_snat").Value())
	assert.Equal(t, "True", cfg.Section("ha").Key("enable").Value())
	assert.Equal(t, "False", cfg.Section("k8s").Key("process_oc_network").Value())
	assert.Equal(t, "10.0.0.0/24", cfg.Section("nsx_v3").Key("container_ip_blocks").Value())
}

func TestAppendErrorIfNotNil(t *testing.T) {
	errs := &[]error{}
	err := error(nil)
	appendErrorIfNotNil(errs, err)
	assert.Empty(t, *errs)

	err = errors.Errorf("test error")
	appendErrorIfNotNil(errs, err)
	assert.Equal(t, 1, len(*errs))
}

func TestFillDefault(t *testing.T) {
	cfg := ini.Empty()
	cfg.NewSections(ncptypes.OperatorSections...)

	fillDefault(cfg, "DEFAULT", "debug", "True", false)
	assert.Equal(t, "True", cfg.Section("DEFAULT").Key("debug").Value())

	fillDefault(cfg, "DEFAULT", "debug", "False", false)
	assert.Equal(t, "True", cfg.Section("DEFAULT").Key("debug").Value())

	fillDefault(cfg, "DEFAULT", "debug", "False", true)
	assert.Equal(t, "False", cfg.Section("DEFAULT").Key("debug").Value())
}

func TestFillClusterNetwork(t *testing.T) {
	cidrs := []string{"10.0.0.0/16", "20.0.0.0/14"}
	mockConfigMap := createMockConfigMap()
	mockNetworkSpec := createMockNetworkSpec(cidrs)
	data := &mockConfigMap.Data
	cfg, _ := ini.Load([]byte((*data)[ncptypes.ConfigMapDataKey]))

	fillClusterNetwork(mockNetworkSpec, cfg)
	assert.Equal(t, "10.0.0.0/16,20.0.0.0/14", cfg.Section("nsx_v3").Key("container_ip_blocks").Value())
}

func TestValidate(t *testing.T) {
	mockConfigMap := &corev1.ConfigMap{}
	mockNetworkSpec := &configv1.NetworkSpec{}
	err := Validate(mockConfigMap, mockNetworkSpec)
	assert.NotNil(t, err)
}

func TestValidateConfig(t *testing.T) {
	cfg := ini.Empty()

	err := validateConfig(cfg, "testSec", "testKey")
	assert.NotNil(t, err)

	cfg.NewSection("testSec")
	err = validateConfig(cfg, "testSec", "testKey")
	assert.NotNil(t, err)

	cfg.Section("testSec").NewKey("testKey", "testValue")
	err = validateConfig(cfg, "testSec", "testKey")
	assert.Nil(t, err)
}

func TestValidateConfigMap(t *testing.T) {
	mockConfigMap := createMockConfigMap()

	errs := validateConfigMap(mockConfigMap)
	assert.Equal(t, 3, len(errs))

	data := &mockConfigMap.Data
	cfg, _ := ini.Load([]byte((*data)[ncptypes.ConfigMapDataKey]))
	cfg.Section("coe").NewKey("cluster", "mockCluster")
	cfg.Section("nsx_v3").NewKey("nsx_api_managers", "mockIP")
	cfg.Section("coe").NewKey("enable_snat", "False")
	cfg.Section("nsx_v3").NewKey("tier0_gateway", "mockT0")
	(*data)[ncptypes.ConfigMapDataKey], _ = iniWriteToString(cfg)

	errs = validateConfigMap(mockConfigMap)
	assert.Empty(t, errs)
}

func TestValidateClusterNetwork(t *testing.T) {
	mockNetworkSpec := &configv1.NetworkSpec{}
	errs := validateClusterNetwork(mockNetworkSpec)
	assert.Equal(t, 1, len(errs))

	mockNetworkSpec.NetworkType = "ncp"
	errs = validateClusterNetwork(mockNetworkSpec)
	assert.Equal(t, 1, len(errs))

	mockNetworkSpec.ClusterNetwork = []configv1.ClusterNetworkEntry{
		configv1.ClusterNetworkEntry{CIDR: "mockCIDR"}}
	errs = validateClusterNetwork(mockNetworkSpec)
	assert.Equal(t, 1, len(errs))

	mockNetworkSpec.ClusterNetwork = []configv1.ClusterNetworkEntry{
		configv1.ClusterNetworkEntry{CIDR: "10.0.0.0/31"}}
	errs = validateClusterNetwork(mockNetworkSpec)
	assert.Equal(t, 2, len(errs))

	mockNetworkSpec.ClusterNetwork = []configv1.ClusterNetworkEntry{
		configv1.ClusterNetworkEntry{
			CIDR:       "10.0.0.0/16",
			HostPrefix: uint32(12)}}
	errs = validateClusterNetwork(mockNetworkSpec)
	assert.Equal(t, 1, len(errs))

	mockNetworkSpec.ClusterNetwork = []configv1.ClusterNetworkEntry{
		configv1.ClusterNetworkEntry{
			CIDR:       "10.0.0.0/16",
			HostPrefix: uint32(24)}}
	errs = validateClusterNetwork(mockNetworkSpec)
	assert.Empty(t, errs)
}

func TestRender(t *testing.T) {
	mockConfigMap := createMockConfigMap()
	objs, err := Render(mockConfigMap)
	assert.Empty(t, objs)
	assert.Error(t, err, "failed to render manifests")
}

func TestNeedApplyChange(t *testing.T) {
	currConfigMap := createMockConfigMap()
	var prevConfigMap *corev1.ConfigMap = nil
	ncpNeedChange, agetnNeedChange, err := NeedApplyChange(currConfigMap, prevConfigMap)
	assert.True(t, ncpNeedChange)
	assert.True(t, agetnNeedChange)
	assert.Nil(t, err)

	prevConfigMap = createMockConfigMap()
	ncpNeedChange, agetnNeedChange, err = NeedApplyChange(currConfigMap, prevConfigMap)
	assert.False(t, ncpNeedChange)
	assert.False(t, agetnNeedChange)
	assert.Nil(t, err)

	data := &prevConfigMap.Data
	cfg, _ := ini.Load([]byte((*data)[ncptypes.ConfigMapDataKey]))

	cfg.Section("nsx_node_agent").NewKey("ovs_uplink_port", "eth1")
	(*data)[ncptypes.ConfigMapDataKey], _ = iniWriteToString(cfg)
	ncpNeedChange, agetnNeedChange, err = NeedApplyChange(currConfigMap, prevConfigMap)
	assert.False(t, ncpNeedChange)
	assert.True(t, agetnNeedChange)
	assert.Nil(t, err)

	cfg.Section("nsx_node_agent").DeleteKey("ovs_uplink_port")
	cfg.Section("DEFAULT").NewKey("debug", "True")
	(*data)[ncptypes.ConfigMapDataKey], _ = iniWriteToString(cfg)
	ncpNeedChange, agetnNeedChange, err = NeedApplyChange(currConfigMap, prevConfigMap)
	assert.True(t, ncpNeedChange)
	assert.True(t, agetnNeedChange)
	assert.Nil(t, err)
}

func TestInSlice(t *testing.T) {
	str := "test"
	strs := []string{"a", "b"}
	assert.False(t, inSlice(str, strs))

	strs = []string{"a", "b", "test"}
	assert.True(t, inSlice(str, strs))
}

func TestGenerateConfigMap(t *testing.T) {
	cfg := ini.Empty()
	cfg.NewSections("sec1", "sec2", "sec3")
	cfg.Section("sec1").NewKey("key1", "val1")
	cfg.Section("sec2").NewKey("key2", "val2")
	cfg.Section("sec3").NewKey("key3", "val3")

	iniString, _ := generateConfigMap(cfg, []string{"sec1", "sec2"})
	destCfg, _ := ini.Load([]byte(iniString))
	assert.Equal(t, []string{"DEFAULT", "sec1", "sec2"}, destCfg.SectionStrings())
	assert.Equal(t, "val1", destCfg.Section("sec1").Key("key1").Value())
	assert.Equal(t, "val2", destCfg.Section("sec2").Key("key2").Value())
	sec, _ := destCfg.GetSection("sec3")
	assert.Nil(t, sec)
}

func TestGenerateOperatorConfigMap(t *testing.T) {
	opConfigMap := createMockConfigMap()
	ncpConfigMap := createMockConfigMap()
	data := &ncpConfigMap.Data
	cfg, _ := ini.Load([]byte((*data)[ncptypes.ConfigMapDataKey]))
	cfg.DeleteSection("nsx_node_agent")
	cfg.Section("nsx_v3").NewKey("nsx_api_managers", "mockIP")
	(*data)[ncptypes.ConfigMapDataKey], _ = iniWriteToString(cfg)
	agentConfigMap := createMockConfigMap()
	data = &agentConfigMap.Data
	cfg, _ = ini.Load([]byte((*data)[ncptypes.ConfigMapDataKey]))
	cfg.DeleteSection("nsx_v3")
	cfg.Section("nsx_node_agent").NewKey("ovs_uplink_port", "eth1")
	(*data)[ncptypes.ConfigMapDataKey], _ = iniWriteToString(cfg)

	GenerateOperatorConfigMap(opConfigMap, ncpConfigMap, agentConfigMap)
	data = &opConfigMap.Data
	cfg, _ = ini.Load([]byte((*data)[ncptypes.ConfigMapDataKey]))
	assert.Equal(t, "mockIP", cfg.Section("nsx_v3").Key("nsx_api_managers").Value())
	assert.Equal(t, "eth1", cfg.Section("nsx_node_agent").Key("ovs_uplink_port").Value())
}

func TestIniWriteToString(t *testing.T) {
	cfg := ini.Empty()
	var buf bytes.Buffer
	cfg.WriteTo(&buf)
	expStr := "\n[DEFAULT]\n" + buf.String()
	retStr, _ := iniWriteToString(cfg)
	assert.Equal(t, expStr, retStr)
}
