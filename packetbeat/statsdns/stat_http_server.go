// Copyright 2020 BlueCat Networks (USA) Inc. and its affiliates
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package statsdns

import (
	"fmt"
	"os/exec"
	"net/http"
	"strings"

	"github.com/elastic/beats/libbeat/logp"
)

func reqAnnouncementDeployFromBam(w http.ResponseWriter, req *http.Request) {
	logp.Debug("HTTP server", "Receive AnnouncementDeployFromBam request")
	ReloadNamedData(true)
}

func onLoadHTTPServer() {
	uriAnnouncementFromBam := fmt.Sprintf("/%v", UrlAnnouncementDeployFromBam)
	logp.Debug("onLoadHTTPServer", "Start Statistic HTTP server")
	// Receive request when postDeploy send request AnnouncementDeployFromBam
	http.HandleFunc(uriAnnouncementFromBam, reqAnnouncementDeployFromBam)
	if err := http.ListenAndServe(StatHTTPServerAddr, nil); err != nil {
		if strings.Contains(err.Error(), "address already in use") {
	        logp.Err("onLoadHTTPServer", err)
			cmd := exec.Command("bash", "-c", "kill -9 $(lsof -t -i:51416)")
			_, err := cmd.Output()
			if err != nil {
				logp.Err("onLoadHTTPServer", err.Error())
				panic(err)
			} else {
				fmt.Printf("TCP Port %q is available", StatHTTPServerAddr[strings.LastIndex(StatHTTPServerAddr, ":")+1:])
				go onLoadHTTPServer()
			}
	    } else {
			panic(err)
			logp.Err("onLoadHTTPServer", err)
	    }
	}
}