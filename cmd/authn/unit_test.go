// Package authn is authentication server for AIStore.
/*
 * Copyright (c) 2018-2022, NVIDIA CORPORATION. All rights reserved.
 */
package main

import (
	"testing"
	"time"

	"github.com/NVIDIA/aistore/api/authn"
	"github.com/NVIDIA/aistore/cmd/authn/tok"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/core/mock"
	"github.com/NVIDIA/aistore/tools/tassert"
)

var (
	users = []string{"user1", "user2", "user3"}
	passs = []string{"pass2", "pass1", "passs"}
)

func init() {
	// Set default expiration time to 30 minutes
	if Conf.Server.ExpirePeriod == 0 {
		Conf.Server.ExpirePeriod = cos.Duration(time.Minute * 30)
	}
}

func createUsers(mgr *mgr, t *testing.T) {
	for idx := range users {
		user := &authn.User{ID: users[idx], Password: passs[idx], Roles: []string{GuestRole}}
		err := mgr.addUser(user)
		if err != nil {
			t.Errorf("Failed to create a user %s: %v", users[idx], err)
		}
	}

	srvUsers, err := mgr.userList()
	tassert.CheckFatal(t, err)
	if len(srvUsers) != len(users)+1 {
		t.Errorf("User count mismatch. Found %d users instead of %d", len(srvUsers), len(users)+1)
	}
	for _, username := range users {
		_, ok := srvUsers[username]
		if !ok {
			t.Errorf("User %q not found", username)
		}
	}
}

func deleteUsers(mgr *mgr, skipNotExist bool, t *testing.T) {
	var err error
	for _, username := range users {
		err = mgr.delUser(username)
		if err != nil {
			if !cos.IsErrNotFound(err) || !skipNotExist {
				t.Errorf("Failed to delete user %s: %v", username, err)
			}
		}
	}
}

func testInvalidUser(mgr *mgr, t *testing.T) {
	user := &authn.User{ID: users[0], Password: passs[1], Roles: []string{GuestRole}}
	err := mgr.addUser(user)
	if err == nil {
		t.Errorf("User with the existing name %s was created: %v", users[0], err)
	}

	nonexisting := "someuser"
	err = mgr.delUser(nonexisting)
	if err == nil {
		t.Errorf("Non-existing user %s was deleted: %v", nonexisting, err)
	}
}

func testUserDelete(mgr *mgr, t *testing.T) {
	const (
		username = "newuser"
		userpass = "newpass"
	)
	user := &authn.User{ID: username, Password: userpass, Roles: []string{GuestRole}}
	err := mgr.addUser(user)
	if err != nil {
		t.Errorf("Failed to create a user %s: %v", username, err)
	}
	srvUsers, err := mgr.userList()
	tassert.CheckFatal(t, err)
	if len(srvUsers) != len(users)+2 {
		t.Errorf("Expected %d users but found %d", len(users)+2, len(srvUsers))
	}

	clu := authn.CluACL{
		ID:    "ABCD",
		Alias: "cluster-test",
		URLs:  []string{"http://localhost:8080"},
	}
	if err := mgr.db.Set(clustersCollection, clu.ID, clu); err != nil {
		t.Error(err)
	}
	defer mgr.delCluster(clu.ID)

	loginMsg := &authn.LoginMsg{ClusterID: clu.Alias}
	token, err := mgr.issueToken(username, userpass, loginMsg)
	if err != nil || token == "" {
		t.Errorf("Failed to generate token for %s: %v", username, err)
	}

	err = mgr.delUser(username)
	if err != nil {
		t.Errorf("Failed to delete user %s: %v", username, err)
	}
	srvUsers, err = mgr.userList()
	tassert.CheckFatal(t, err)
	if len(srvUsers) != len(users)+1 {
		t.Errorf("Expected %d users but found %d", len(users)+1, len(srvUsers))
	}
	token, err = mgr.issueToken(username, userpass, loginMsg)
	if err == nil {
		t.Errorf("Token issued for deleted user  %s: %v", username, token)
	} else if err != errInvalidCredentials {
		t.Errorf("Invalid error: %v", err)
	}
}

func TestManager(t *testing.T) {
	driver := mock.NewDBDriver()
	// NOTE: new manager initailizes users DB and adds a default user as a Guest
	mgr, err := newMgr(driver)
	tassert.CheckError(t, err)
	createUsers(mgr, t)
	testInvalidUser(mgr, t)
	testUserDelete(mgr, t)
	deleteUsers(mgr, false, t)
}

func TestToken(t *testing.T) {
	if testing.Short() {
		t.Skipf("skipping %s in short mode", t.Name())
	}
	var (
		err    error
		token  string
		secret = Conf.Server.Secret
	)

	driver := mock.NewDBDriver()
	mgr, err := newMgr(driver)
	tassert.CheckFatal(t, err)
	createUsers(mgr, t)
	defer deleteUsers(mgr, false, t)

	clu := authn.CluACL{
		ID:    "ABCD",
		Alias: "cluster-test",
		URLs:  []string{"http://localhost:8080"},
	}
	if err := mgr.db.Set(clustersCollection, clu.ID, clu); err != nil {
		t.Error(err)
	}
	defer mgr.delCluster(clu.ID)

	// correct user creds
	shortExpiration := 2 * time.Second
	loginMsg := &authn.LoginMsg{ClusterID: clu.Alias, ExpiresIn: &shortExpiration}
	token, err = mgr.issueToken(users[1], passs[1], loginMsg)
	if err != nil || token == "" {
		t.Errorf("Failed to generate token for %s: %v", users[1], err)
	}
	info, err := tok.DecryptToken(token, secret)
	if err != nil {
		t.Fatalf("Failed to decript token %v: %v", token, err)
	}
	if info.UserID != users[1] {
		t.Errorf("Invalid user %s returned for token of %s", info.UserID, users[1])
	}

	// incorrect user creds
	loginMsg = &authn.LoginMsg{}
	tokenInval, err := mgr.issueToken(users[1], passs[0], loginMsg)
	if tokenInval != "" || err == nil {
		t.Errorf("Some token generated for incorrect user creds: %v", tokenInval)
	}

	// expired token test
	time.Sleep(shortExpiration)
	tk, err := tok.DecryptToken(token, secret)
	tassert.CheckFatal(t, err)
	if tk.Expires.After(time.Now()) {
		t.Fatalf("Token must be expired: %s", token)
	}
}

func TestMergeCluACLS(t *testing.T) {
	tests := []struct {
		title    string
		cluFlt   string
		toACLs   cluACLList
		fromACLs cluACLList
		resACLs  cluACLList
	}{
		{
			title: "The same lists",
			toACLs: []*authn.CluACL{
				{
					ID:     "1234",
					Alias:  "one",
					Access: 20,
				},
				{
					ID:     "5678",
					Alias:  "second",
					Access: 20,
				},
			},
			fromACLs: []*authn.CluACL{
				{
					ID:     "1234",
					Alias:  "one",
					Access: 20,
				},
			},
			resACLs: []*authn.CluACL{
				{
					ID:     "1234",
					Alias:  "one",
					Access: 20,
				},
				{
					ID:     "5678",
					Alias:  "second",
					Access: 20,
				},
			},
		},
		{
			title: "Update permissions only",
			toACLs: []*authn.CluACL{
				{
					ID:     "1234",
					Alias:  "one",
					Access: 20,
				},
				{
					ID:     "5678",
					Alias:  "second",
					Access: 20,
				},
			},
			fromACLs: []*authn.CluACL{
				{
					ID:     "1234",
					Alias:  "one",
					Access: 40,
				},
			},
			resACLs: []*authn.CluACL{
				{
					ID:     "1234",
					Alias:  "one",
					Access: 40,
				},
				{
					ID:     "5678",
					Alias:  "second",
					Access: 20,
				},
			},
		},
		{
			title: "Append new cluster",
			toACLs: []*authn.CluACL{
				{
					ID:     "1234",
					Alias:  "one",
					Access: 20,
				},
				{
					ID:     "5678",
					Alias:  "second",
					Access: 20,
				},
			},
			fromACLs: []*authn.CluACL{
				{
					ID:     "abcde",
					Alias:  "third",
					Access: 40,
				},
				{
					ID:     "hijk",
					Alias:  "fourth",
					Access: 40,
				},
			},
			resACLs: []*authn.CluACL{
				{
					ID:     "1234",
					Alias:  "one",
					Access: 20,
				},
				{
					ID:     "5678",
					Alias:  "second",
					Access: 20,
				},
				{
					ID:     "abcde",
					Alias:  "third",
					Access: 40,
				},
				{
					ID:     "hijk",
					Alias:  "fourth",
					Access: 40,
				},
			},
		},
		{
			title: "Update permissions for existing cluster and apend new ones",
			toACLs: []*authn.CluACL{
				{
					ID:     "1234",
					Alias:  "one",
					Access: 20,
				},
				{
					ID:     "5678",
					Alias:  "second",
					Access: 20,
				},
			},
			fromACLs: []*authn.CluACL{
				{
					ID:     "1234",
					Alias:  "one",
					Access: 40,
				},
				{
					ID:     "abcde",
					Alias:  "third",
					Access: 60,
				},
			},
			resACLs: []*authn.CluACL{
				{
					ID:     "1234",
					Alias:  "one",
					Access: 40,
				},
				{
					ID:     "5678",
					Alias:  "second",
					Access: 20,
				},
				{
					ID:     "abcde",
					Alias:  "third",
					Access: 60,
				},
			},
		},
		{
			title:  "Append only 'abcde' cluster",
			cluFlt: "abcde",
			toACLs: []*authn.CluACL{
				{
					ID:     "1234",
					Alias:  "one",
					Access: 20,
				},
				{
					ID:     "5678",
					Alias:  "second",
					Access: 20,
				},
			},
			fromACLs: []*authn.CluACL{
				{
					ID:     "abcde",
					Alias:  "third",
					Access: 40,
				},
				{
					ID:     "hijk",
					Alias:  "fourth",
					Access: 40,
				},
			},
			resACLs: []*authn.CluACL{
				{
					ID:     "1234",
					Alias:  "one",
					Access: 20,
				},
				{
					ID:     "5678",
					Alias:  "second",
					Access: 20,
				},
				{
					ID:     "abcde",
					Alias:  "third",
					Access: 40,
				},
			},
		},
	}
	for _, test := range tests {
		res := mergeClusterACLs(test.toACLs, test.fromACLs, test.cluFlt)
		for i, r := range res {
			if r.String() != test.resACLs[i].String() || r.Access != test.resACLs[i].Access {
				t.Errorf("%s[filter: %s]: %v[%v] != %v[%v]", test.title, test.cluFlt, r, r.Access, test.resACLs[i], test.resACLs[i].Access)
			}
		}
	}
}

func newBck(name, provider, uuid string) cmn.Bck {
	return cmn.Bck{
		Name:     name,
		Provider: provider,
		Ns:       cmn.Ns{UUID: uuid},
	}
}

func TestMergeBckACLS(t *testing.T) {
	tests := []struct {
		title    string
		cluFlt   string
		toACLs   bckACLList
		fromACLs bckACLList
		resACLs  bckACLList
	}{
		{
			title: "Nothing to update",
			toACLs: []*authn.BckACL{
				{
					Bck:    newBck("bck", "ais", "1234"),
					Access: 20,
				},
				{
					Bck:    newBck("bck", "ais", "5678"),
					Access: 20,
				},
			},
			fromACLs: []*authn.BckACL{
				{
					Bck:    newBck("bck", "ais", "1234"),
					Access: 20,
				},
			},
			resACLs: []*authn.BckACL{
				{
					Bck:    newBck("bck", "ais", "1234"),
					Access: 20,
				},
				{
					Bck:    newBck("bck", "ais", "5678"),
					Access: 20,
				},
			},
		},
		{
			title: "Update permissions only",
			toACLs: []*authn.BckACL{
				{
					Bck:    newBck("bck", "ais", "1234"),
					Access: 20,
				},
				{
					Bck:    newBck("bck", "ais", "5678"),
					Access: 20,
				},
			},
			fromACLs: []*authn.BckACL{
				{
					Bck:    newBck("bck", "ais", "5678"),
					Access: 40,
				},
			},
			resACLs: []*authn.BckACL{
				{
					Bck:    newBck("bck", "ais", "1234"),
					Access: 20,
				},
				{
					Bck:    newBck("bck", "ais", "5678"),
					Access: 40,
				},
			},
		},
		{
			title: "Append new buckets",
			toACLs: []*authn.BckACL{
				{
					Bck:    newBck("bck", "ais", "1234"),
					Access: 20,
				},
				{
					Bck:    newBck("bck", "ais", "5678"),
					Access: 20,
				},
			},
			fromACLs: []*authn.BckACL{
				{
					Bck:    newBck("bck", "ais", "5678"),
					Access: 30,
				},
				{
					Bck:    newBck("bck", "aws", "5678"),
					Access: 40,
				},
				{
					Bck:    newBck("bck1", "ais", "1234"),
					Access: 50,
				},
			},
			resACLs: []*authn.BckACL{
				{
					Bck:    newBck("bck", "ais", "1234"),
					Access: 20,
				},
				{
					Bck:    newBck("bck", "ais", "5678"),
					Access: 30,
				},
				{
					Bck:    newBck("bck", "aws", "5678"),
					Access: 40,
				},
				{
					Bck:    newBck("bck1", "ais", "1234"),
					Access: 50,
				},
			},
		},
		{
			title: "Update permissions for existing buckets and apend new ones",
			toACLs: []*authn.BckACL{
				{
					Bck:    newBck("bck", "ais", "1234"),
					Access: 20,
				},
				{
					Bck:    newBck("bck", "ais", "5678"),
					Access: 20,
				},
			},
			fromACLs: []*authn.BckACL{
				{
					Bck:    newBck("bck2", "ais", "1234"),
					Access: 20,
				},
				{
					Bck:    newBck("bck", "ais", "1234"),
					Access: 70,
				},
			},
			resACLs: []*authn.BckACL{
				{
					Bck:    newBck("bck", "ais", "1234"),
					Access: 70,
				},
				{
					Bck:    newBck("bck", "ais", "5678"),
					Access: 20,
				},
				{
					Bck:    newBck("bck2", "ais", "1234"),
					Access: 20,
				},
			},
		},
		{
			title:  "Append and update buckets of '5678' cluster only",
			cluFlt: "5678",
			toACLs: []*authn.BckACL{
				{
					Bck:    newBck("bck", "ais", "1234"),
					Access: 20,
				},
				{
					Bck:    newBck("bck", "ais", "5678"),
					Access: 20,
				},
			},
			fromACLs: []*authn.BckACL{
				{
					Bck:    newBck("bck2", "ais", "5678"),
					Access: 60,
				},
				{
					Bck:    newBck("bck2", "ais", "1234"),
					Access: 70,
				},
				{
					Bck:    newBck("bck", "ais", "5678"),
					Access: 90,
				},
			},
			resACLs: []*authn.BckACL{
				{
					Bck:    newBck("bck", "ais", "1234"),
					Access: 20,
				},
				{
					Bck:    newBck("bck", "ais", "5678"),
					Access: 90,
				},
				{
					Bck:    newBck("bck2", "ais", "5678"),
					Access: 60,
				},
			},
		},
	}
	for _, test := range tests {
		res := mergeBckACLs(test.toACLs, test.fromACLs, test.cluFlt)
		for i, r := range res {
			if !r.Bck.Equal(&test.resACLs[i].Bck) || r.Access != test.resACLs[i].Access {
				t.Errorf("%s[filter: %s]: %v[%v] != %v[%v]", test.title, test.cluFlt, r.Bck, r.Access, test.resACLs[i], test.resACLs[i].Access)
			}
		}
	}
}
