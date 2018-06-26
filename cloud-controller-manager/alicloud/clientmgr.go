/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package alicloud

import (
	"sync"
	"time"

	"github.com/denverdino/aliyungo/ecs"
	"github.com/denverdino/aliyungo/metadata"
	"github.com/denverdino/aliyungo/slb"
	"github.com/golang/glog"
	"github.com/patrickmn/go-cache"
	"k8s.io/apimachinery/pkg/util/wait"
	"fmt"
	"strings"
)

var ROLE_NAME = "KubernetesMasterRole"

var TOKEN_RESYNC_PERIOD = 5 * time.Minute

type TokenAuth struct {
	lock   sync.RWMutex
	auth   metadata.RoleAuth
	active bool
}

func (token *TokenAuth) authid() (string, string, string) {
	token.lock.RLock()
	defer token.lock.RUnlock()

	return token.auth.AccessKeyId,
		token.auth.AccessKeySecret,
		token.auth.SecurityToken
}

type ClientMgr struct {
	stop <-chan struct{}

	token *TokenAuth

	meta         IMetaData
	routes       *RoutesClient
	loadbalancer *LoadBalancerClient
	instance     *InstanceClient
}

func NewClientMgr(key, secret string) (*ClientMgr, error) {
	token := &TokenAuth{
		auth: metadata.RoleAuth{
			AccessKeyId:     key,
			AccessKeySecret: secret,
		},
		active: false,
	}
	m := NewMetaData()

	if key == "" || secret == "" {
		if rolename, err := m.RoleName(); err != nil {
			return nil, err
		} else {
			ROLE_NAME = rolename
			role, err := m.RamRoleToken(ROLE_NAME)
			if err != nil {
				return nil, err
			}
			glog.V(2).Infof("alicloud: clientmgr, using role=[%s] with initial token=[%+v]", ROLE_NAME, role)
			token.auth = role
			token.active = true
		}
	}
	keyid, sec, tok := token.authid()
	ecsclient := ecs.NewECSClientWithSecurityToken(keyid, sec, tok, DEFAULT_REGION)
	ecsclient.SetUserAgent(KUBERNETES_ALICLOUD_IDENTITY)
	slbclient := slb.NewSLBClientWithSecurityToken(keyid, sec, tok, DEFAULT_REGION)
	slbclient.SetUserAgent(KUBERNETES_ALICLOUD_IDENTITY)

	mgr := &ClientMgr{
		stop:  make(<-chan struct{}, 1),
		token: token,
		meta:  m,
		instance: &InstanceClient{
			c: ecsclient,
		},
		loadbalancer: &LoadBalancerClient{
			c: slbclient,
		},
		routes: &RoutesClient{
			client:  ecsclient,
			routers: cache.New(defaultCacheExpiration, defaultCacheExpiration),
			vpcs:    cache.New(defaultCacheExpiration, defaultCacheExpiration),
		},
	}
	if !token.active {
		// use key and secret
		glog.Infof("alicloud: clientmgr, use accesskeyid and accesskeysecret mode to authenticate user. without token")
		return mgr, nil
	}
	go wait.Until(func() {
		// refresh client token periodically
		token.lock.Lock()
		defer token.lock.Unlock()
		role, err := mgr.meta.RamRoleToken(ROLE_NAME)
		if err != nil {
			glog.Errorf("alicloud: clientmgr, error get ram role token [%s]\n", err.Error())
			return
		}
		token.auth = role
		ecsclient.WithSecurityToken(role.SecurityToken).
			WithAccessKeyId(role.AccessKeyId).
			WithAccessKeySecret(role.AccessKeySecret)
		slbclient.WithSecurityToken(role.SecurityToken).
			WithAccessKeyId(role.AccessKeyId).
			WithAccessKeySecret(role.AccessKeySecret)
	}, time.Duration(TOKEN_RESYNC_PERIOD), mgr.stop)

	return mgr, nil
}

func (c *ClientMgr) Instances() *InstanceClient {
	return c.instance
}

func (c *ClientMgr) Routes() *RoutesClient {
	return c.routes
}

func (c *ClientMgr) LoadBalancers() *LoadBalancerClient {
	return c.loadbalancer
}

func (c *ClientMgr) MetaData() IMetaData {

	return c.meta
}

type IMetaData interface {
	HostName()(string, error)
	ImageID() (string, error)
	InstanceID() (string, error)
	Mac() (string, error)
	NetworkType() (string, error)
	OwnerAccountID() (string, error)
	PrivateIPv4() (string, error)
	Region() (string, error)
	SerialNumber() (string, error)
	SourceAddress() (string, error)
	VpcCIDRBlock() (string, error)
	VpcID() (string, error)
	VswitchCIDRBlock() (string, error)
	Zone() (string, error)
	NTPConfigServers() ([]string, error)
	RoleName() (string, error)
	RamRoleToken(role string) (metadata.RoleAuth, error)
	VswitchID() (string, error)
}

func NewMetaData() IMetaData{
	if cfg.Global.Region != "" &&
		cfg.Global.VpcID != "" &&
		cfg.Global.VswitchID != "" &&
		cfg.Global.ZoneID != "" {
		glog.V(2).Infof("use mocked metadata server.")
		return &fakeMetaData{base: metadata.NewMetaData(nil)}
	}
	return metadata.NewMetaData(nil)
}

type fakeMetaData struct {
	base 	IMetaData
}

func (m *fakeMetaData) HostName() (string, error) {

	return "",fmt.Errorf("unimplemented")
}

func (m *fakeMetaData) ImageID() (string, error) {

	return "",fmt.Errorf("unimplemented")
}

func (m *fakeMetaData) InstanceID() (string, error) {

	return "fakedInstanceid",nil
}

func (m *fakeMetaData) Mac() (string, error) {

	return "",fmt.Errorf("unimplemented")
}

func (m *fakeMetaData) NetworkType() (string, error) {

	return "",fmt.Errorf("unimplemented")
}

func (m *fakeMetaData) OwnerAccountID() (string, error) {

	return "",fmt.Errorf("unimplemented")
}

func (m *fakeMetaData) PrivateIPv4() (string, error) {

	return "",fmt.Errorf("unimplemented")
}

func (m *fakeMetaData) Region() (string, error) {
	if cfg.Global.Region != "" {
		return cfg.Global.Region, nil
	}
	return m.base.Region()
}

func (m *fakeMetaData) SerialNumber() (string, error) {

	return "",fmt.Errorf("unimplemented")
}

func (m *fakeMetaData) SourceAddress() (string, error) {

	return "",fmt.Errorf("unimplemented")

}

func (m *fakeMetaData) VpcCIDRBlock() (string, error) {

	return "",fmt.Errorf("unimplemented")
}

func (m *fakeMetaData) VpcID() (string, error) {

	return cfg.Global.VpcID,nil
}

func (m *fakeMetaData) VswitchCIDRBlock() (string, error) {

	return "",fmt.Errorf("unimplemented")
}

// zone1:vswitchid1,zone2:vswitch2
func (m *fakeMetaData) VswitchID() (string, error) {

	zlist := strings.Split(cfg.Global.VswitchID,",")
	if len(zlist) == 1 {
		glog.Infof("simple vswitchid mode, %s",cfg.Global.VswitchID)
		return cfg.Global.VswitchID,nil
	}
	zone, err := m.Zone()
	if err != nil {
		return "",fmt.Errorf("retrieve vswitchid error for %s",err.Error())
	}
	for _, zone := range zlist {
		vs := strings.Split(zone,":")
		if len(vs) != 2 {
			return "", fmt.Errorf("cloud-config vswitch format error: %s",cfg.Global.VswitchID)
		}
		if vs[0] == zone {
			return vs[1], nil
		}
	}
	glog.Infof("zone[%s] match failed, fallback with simple vswitch id mode, [%s]",zone,cfg.Global.VswitchID)
	return cfg.Global.VswitchID, nil
}

func (m *fakeMetaData) EIPv4() (string, error) {

	return "",fmt.Errorf("unimplemented")
}

func (m *fakeMetaData) DNSNameServers() ([]string, error) {

	return []string{""},fmt.Errorf("unimplemented")
}

func (m *fakeMetaData) NTPConfigServers() ([]string, error) {

	return []string{""},fmt.Errorf("unimplemented")
}

func (m *fakeMetaData) Zone() (string, error) {
	if cfg.Global.ZoneID != "" {
		return cfg.Global.ZoneID, nil
	}
	return m.base.Zone()
}

func (m *fakeMetaData) RoleName() (string, error) {

	return "",fmt.Errorf("unimplemented")
}

func (m *fakeMetaData) RamRoleToken(role string) (metadata.RoleAuth, error) {

	return metadata.RoleAuth{},fmt.Errorf("unimplemented")
}

