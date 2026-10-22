// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Authors of KubeArmor

package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubearmor/KubeArmor/pkg/KubeArmorOperator/common"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestSetCriSocket(t *testing.T) {
	tests := []struct {
		name    string
		runtime string
		label   string
		args    []string
		want    []string
	}{
		{
			name:    "k3s socket is passed with its host path",
			runtime: "containerd",
			label:   "run_k3s_containerd_containerd.sock",
			args:    []string{"-gRPC=32767"},
			want:    []string{"-gRPC=32767", "-criSocket=unix:///run/k3s/containerd/containerd.sock"},
		},
		{
			name:    "replaces an existing flag",
			runtime: "cri-o",
			label:   "var_run_crio_crio.sock",
			args:    []string{"-criSocket=unix:///run/other.sock", "-gRPC=32767"},
			want:    []string{"-criSocket=unix:///var/run/crio/crio.sock", "-gRPC=32767"},
		},
		{
			name:    "unknown socket leaves args untouched",
			runtime: "containerd",
			label:   "no_such_socket",
			args:    []string{"-gRPC=32767"},
			want:    []string{"-gRPC=32767"},
		},
		{
			name:    "unknown runtime leaves args untouched",
			runtime: "none",
			label:   "run_k3s_containerd_containerd.sock",
			args:    []string{"-gRPC=32767"},
			want:    []string{"-gRPC=32767"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := tt.args
			setCriSocket(tt.runtime, tt.label, &args)
			assert.Equal(t, tt.want, args)
		})
	}

	assert.NotPanics(t, func() { setCriSocket("containerd", "run_k3s_containerd_containerd.sock", nil) })
}

// The path passed via -criSocket must exist inside the container, i.e. it has to
// live under the directory that genRuntimeVolumes mounts.
func TestCriSocketIsUnderMountedDir(t *testing.T) {
	for runtime, sockets := range common.ContainerRuntimeSocketMap {
		if runtime == "nri" {
			continue
		}
		for _, socket := range sockets {
			label := strings.ReplaceAll(socket[1:], "/", "_")

			vols, mounts := genRuntimeVolumes(runtime, label, "")
			if assert.Len(t, vols, 1, socket) && assert.Len(t, mounts, 1, socket) {
				assert.Equal(t, vols[0].HostPath.Path, mounts[0].MountPath, "%s: mounted at a different path than the host path", socket)
				assert.True(t, strings.HasPrefix(socket, mounts[0].MountPath+"/"), "%s is not under %s", socket, mounts[0].MountPath)
			}

			var args []string
			setCriSocket(runtime, label, &args)
			assert.Equal(t, []string{"-criSocket=unix://" + socket}, args)
		}
	}
}

// A warn log on a failed provider call must render the error text. A printf
// verb that does not fit an error prints a format dump like %!w(...) instead.
func TestFetchClusterNameWarnLogFormat(t *testing.T) {
	newWatcher := func() (*ClusterWatcher, *observer.ObservedLogs) {
		core, logs := observer.New(zap.WarnLevel)
		return &ClusterWatcher{Log: zap.New(core).Sugar()}, logs
	}

	t.Run("gke request creation failure", func(t *testing.T) {
		cw, logs := newWatcher()
		_, err := cw.fetchClusterNameFromGKE("://invalid", "/computeMetadata/v1/instance/attributes/cluster-name")
		assert.Error(t, err)
		if assert.Len(t, logs.All(), 1) {
			assert.Equal(t, `failed to create request: parse "://invalid/computeMetadata/v1/instance/attributes/cluster-name": missing protocol scheme, check provider host name and endpoint`, logs.All()[0].Message)
		}
	})

	t.Run("gke request failure", func(t *testing.T) {
		cw, logs := newWatcher()
		_, err := cw.fetchClusterNameFromGKE("http://127.0.0.1:1", "/computeMetadata/v1/instance/attributes/cluster-name")
		assert.Error(t, err)
		if assert.Len(t, logs.All(), 1) {
			msg := logs.All()[0].Message
			assert.True(t, strings.HasPrefix(msg, `error making request: Get "http://127.0.0.1:1/computeMetadata/v1/instance/attributes/cluster-name": `), msg)
			assert.Contains(t, msg, "connection refused")
			assert.NotContains(t, msg, "%!")
			assert.True(t, strings.HasSuffix(msg, ", check provider host name and endpoint"), msg)
		}
	})

	t.Run("gke response body read failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "10")
			w.Write([]byte("x"))
		}))
		defer srv.Close()
		cw, logs := newWatcher()
		_, err := cw.fetchClusterNameFromGKE(srv.URL, "/computeMetadata/v1/instance/attributes/cluster-name")
		assert.Error(t, err)
		if assert.Len(t, logs.All(), 1) {
			assert.Equal(t, `error reading response body: unexpected EOF`, logs.All()[0].Message)
		}
	})

	t.Run("aws token request creation failure", func(t *testing.T) {
		cw, logs := newWatcher()
		_, err := cw.fetchClusterNameFromAWS("://invalid", "/latest/user-data")
		assert.Error(t, err)
		if assert.Len(t, logs.All(), 1) {
			assert.Equal(t, `failed to create request for fetching token: parse "://invalid/latest/api/token": missing protocol scheme, check provider host name`, logs.All()[0].Message)
		}
	})

	t.Run("aws token request failure", func(t *testing.T) {
		cw, logs := newWatcher()
		_, err := cw.fetchClusterNameFromAWS("http://127.0.0.1:1", "/latest/user-data")
		assert.Error(t, err)
		if assert.Len(t, logs.All(), 1) {
			msg := logs.All()[0].Message
			assert.True(t, strings.HasPrefix(msg, `error making request: Put "http://127.0.0.1:1/latest/api/token": `), msg)
			assert.Contains(t, msg, "connection refused")
			assert.NotContains(t, msg, "%!")
		}
	})

	t.Run("aws metadata read failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut {
				w.Write([]byte("token"))
				return
			}
			w.Header().Set("Content-Length", "10")
			w.Write([]byte("x"))
		}))
		defer srv.Close()
		cw, logs := newWatcher()
		_, err := cw.fetchClusterNameFromAWS(srv.URL, "/latest/user-data")
		assert.Error(t, err)
		if assert.Len(t, logs.All(), 1) {
			assert.Equal(t, `failed to read metadata: unexpected EOF`, logs.All()[0].Message)
		}
	})

	t.Run("aws token read failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "10")
			w.Write([]byte("x"))
		}))
		defer srv.Close()
		cw, logs := newWatcher()
		_, err := cw.fetchClusterNameFromAWS(srv.URL, "/latest/user-data")
		assert.Error(t, err)
		if assert.Len(t, logs.All(), 1) {
			assert.Equal(t, `failed to read token: unexpected EOF`, logs.All()[0].Message)
		}
	})
}
