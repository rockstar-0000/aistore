// Package api provides AIStore API over HTTP(S)
/*
 * Copyright (c) 2018-2021, NVIDIA CORPORATION. All rights reserved.
 */
package api

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/NVIDIA/aistore/authn"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	jsoniter "github.com/json-iterator/go"
)

type (
	AuthnSpec struct {
		AdminName     string
		AdminPassword string
	}
)

func AddUser(baseParams BaseParams, newUser *authn.User) error {
	msg, err := jsoniter.Marshal(newUser)
	if err != nil {
		return err
	}
	baseParams.Method = http.MethodPost
	reqParams := &ReqParams{
		BaseParams: baseParams,
		Path:       cmn.URLPathUsers.S,
		Body:       msg,
		Header:     http.Header{cmn.HdrContentType: []string{cmn.ContentJSON}},
	}
	return reqParams.DoHTTPRequest()
}

func UpdateUser(baseParams BaseParams, newUser *authn.User) error {
	msg := cos.MustMarshal(newUser)
	baseParams.Method = http.MethodPut
	reqParams := &ReqParams{
		BaseParams: baseParams,
		Path:       cmn.URLPathUsers.Join(newUser.ID),
		Body:       msg,
		Header:     http.Header{cmn.HdrContentType: []string{cmn.ContentJSON}},
	}
	return reqParams.DoHTTPRequest()
}

func DeleteUser(baseParams BaseParams, userID string) error {
	baseParams.Method = http.MethodDelete
	reqParams := &ReqParams{BaseParams: baseParams, Path: cmn.URLPathUsers.Join(userID)}
	return reqParams.DoHTTPRequest()
}

// Authorize a user and return a user token in case of success.
// The token expires in `expire` time. If `expire` is `nil` the expiration
// time is set by AuthN (default AuthN expiration time is 24 hours)
func LoginUser(baseParams BaseParams, userID, pass string, expire *time.Duration) (token *authn.TokenMsg, err error) {
	baseParams.Method = http.MethodPost
	rec := authn.LoginMsg{Password: pass, ExpiresIn: expire}
	reqParams := &ReqParams{
		BaseParams: baseParams,
		Path:       cmn.URLPathUsers.Join(userID),
		Body:       cos.MustMarshal(rec),
		Header:     http.Header{cmn.HdrContentType: []string{cmn.ContentJSON}},
	}
	err = reqParams.DoHTTPReqResp(&token)
	if err != nil {
		return nil, err
	}

	if token.Token == "" {
		return nil, errors.New("login failed: empty response from AuthN server")
	}
	return token, nil
}

func RegisterClusterAuthN(baseParams BaseParams, cluSpec authn.Cluster) error {
	msg := cos.MustMarshal(cluSpec)
	baseParams.Method = http.MethodPost
	reqParams := &ReqParams{
		BaseParams: baseParams,
		Path:       cmn.URLPathClusters.S,
		Body:       msg,
		Header:     http.Header{cmn.HdrContentType: []string{cmn.ContentJSON}},
	}
	return reqParams.DoHTTPRequest()
}

func UpdateClusterAuthN(baseParams BaseParams, cluSpec authn.Cluster) error {
	msg := cos.MustMarshal(cluSpec)
	baseParams.Method = http.MethodPut
	reqParams := &ReqParams{
		BaseParams: baseParams,
		Path:       cmn.URLPathClusters.Join(cluSpec.ID),
		Body:       msg,
		Header:     http.Header{cmn.HdrContentType: []string{cmn.ContentJSON}},
	}
	return reqParams.DoHTTPRequest()
}

func UnregisterClusterAuthN(baseParams BaseParams, spec authn.Cluster) error {
	baseParams.Method = http.MethodDelete
	reqParams := &ReqParams{BaseParams: baseParams, Path: cmn.URLPathClusters.Join(spec.ID)}
	return reqParams.DoHTTPRequest()
}

func GetClusterAuthN(baseParams BaseParams, spec authn.Cluster) ([]*authn.Cluster, error) {
	baseParams.Method = http.MethodGet
	path := cmn.URLPathClusters.S
	if spec.ID != "" {
		path = cos.JoinWords(path, spec.ID)
	}
	clusters := &authn.ClusterList{}
	reqParams := &ReqParams{BaseParams: baseParams, Path: path}
	err := reqParams.DoHTTPReqResp(clusters)

	rec := make([]*authn.Cluster, 0, len(clusters.Clusters))
	for _, clu := range clusters.Clusters {
		rec = append(rec, clu)
	}
	less := func(i, j int) bool { return rec[i].ID < rec[j].ID }
	sort.Slice(rec, less)
	return rec, err
}

func GetRoleAuthN(baseParams BaseParams, roleID string) (*authn.Role, error) {
	if roleID == "" {
		return nil, errors.New("missing role ID")
	}
	rInfo := &authn.Role{}
	baseParams.Method = http.MethodGet
	reqParams := &ReqParams{BaseParams: baseParams, Path: cos.JoinWords(cmn.URLPathRoles.S, roleID)}
	err := reqParams.DoHTTPReqResp(&rInfo)
	return rInfo, err
}

func GetRolesAuthN(baseParams BaseParams) ([]*authn.Role, error) {
	baseParams.Method = http.MethodGet
	path := cmn.URLPathRoles.S
	roles := make([]*authn.Role, 0)
	reqParams := &ReqParams{BaseParams: baseParams, Path: path}
	err := reqParams.DoHTTPReqResp(&roles)

	less := func(i, j int) bool { return roles[i].Name < roles[j].Name }
	sort.Slice(roles, less)
	return roles, err
}

func GetUsersAuthN(baseParams BaseParams) ([]*authn.User, error) {
	baseParams.Method = http.MethodGet
	users := make(map[string]*authn.User, 4)
	reqParams := &ReqParams{BaseParams: baseParams, Path: cmn.URLPathUsers.S}
	err := reqParams.DoHTTPReqResp(&users)

	list := make([]*authn.User, 0, len(users))
	for _, info := range users {
		list = append(list, info)
	}

	less := func(i, j int) bool { return list[i].ID < list[j].ID }
	sort.Slice(list, less)

	return list, err
}

func GetUserAuthN(baseParams BaseParams, userID string) (*authn.User, error) {
	if userID == "" {
		return nil, errors.New("missing user ID")
	}
	uInfo := &authn.User{}
	baseParams.Method = http.MethodGet
	reqParams := &ReqParams{BaseParams: baseParams, Path: cos.JoinWords(cmn.URLPathUsers.S, userID)}
	err := reqParams.DoHTTPReqResp(&uInfo)
	return uInfo, err
}

func AddRoleAuthN(baseParams BaseParams, roleSpec *authn.Role) error {
	msg := cos.MustMarshal(roleSpec)
	baseParams.Method = http.MethodPost
	reqParams := &ReqParams{
		BaseParams: baseParams,
		Path:       cmn.URLPathRoles.S,
		Body:       msg,
		Header:     http.Header{cmn.HdrContentType: []string{cmn.ContentJSON}},
	}
	return reqParams.DoHTTPRequest()
}

func DeleteRoleAuthN(baseParams BaseParams, role string) error {
	baseParams.Method = http.MethodDelete
	reqParams := &ReqParams{BaseParams: baseParams, Path: cmn.URLPathRoles.Join(role)}
	return reqParams.DoHTTPRequest()
}

func RevokeToken(baseParams BaseParams, token string) error {
	baseParams.Method = http.MethodDelete
	msg := &authn.TokenMsg{Token: token}
	reqParams := &ReqParams{
		Body:       cos.MustMarshal(msg),
		BaseParams: baseParams,
		Path:       cmn.URLPathTokens.S,
		Header:     http.Header{cmn.HdrContentType: []string{cmn.ContentJSON}},
	}
	return reqParams.DoHTTPRequest()
}

func GetAuthNConfig(baseParams BaseParams) (*authn.Config, error) {
	conf := &authn.Config{}
	baseParams.Method = http.MethodGet
	reqParams := ReqParams{BaseParams: baseParams, Path: cmn.URLPathDaemon.S}
	err := reqParams.DoHTTPReqResp(&conf)
	return conf, err
}

func SetAuthNConfig(baseParams BaseParams, conf *authn.ConfigToUpdate) error {
	baseParams.Method = http.MethodPut
	reqParams := &ReqParams{
		Body:       cos.MustMarshal(conf),
		BaseParams: baseParams,
		Path:       cmn.URLPathDaemon.S,
		Header:     http.Header{cmn.HdrContentType: []string{cmn.ContentJSON}},
	}
	return reqParams.DoHTTPRequest()
}
