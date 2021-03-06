package pbs

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang/glog"
	"github.com/julienschmidt/httprouter"
	"github.com/prebid/prebid-server/analytics"
	"github.com/prebid/prebid-server/config"
	"github.com/prebid/prebid-server/pbsmetrics"
	"github.com/prebid/prebid-server/ssl"
	"github.com/prebid/prebid-server/usersync"
)

// Recaptcha code from https://github.com/haisum/recaptcha/blob/master/recaptcha.go
const RECAPTCHA_URL = "https://www.google.com/recaptcha/api/siteverify"

const (
	USERSYNC_OPT_OUT     = "usersync.opt_outs"
	USERSYNC_BAD_REQUEST = "usersync.bad_requests"
	USERSYNC_SUCCESS     = "usersync.%s.sets"
)

type HostCookieSettings struct {
	Domain       string
	Family       string
	CookieName   string
	OptOutURL    string
	OptInURL     string
	OptOutCookie config.Cookie
	TTL          time.Duration
}

// uidWithExpiry bundles the UID with an Expiration date.
// After the expiration, the UID is no longer valid.
type uidWithExpiry struct {
	// UID is the ID given to a user by a particular bidder
	UID string `json:"uid"`
	// Expires is the time at which this UID should no longer apply.
	Expires time.Time `json:"expires"`
}

type UserSyncDeps struct {
	ExternalUrl        string
	RecaptchaSecret    string
	HostCookieSettings *HostCookieSettings
	MetricsEngine      pbsmetrics.MetricsEngine
	PBSAnalytics       analytics.PBSAnalyticsModule
}

// pbsCookieJson defines the JSON contract for the cookie data's storage format.
//
// This exists so that PBSCookie (which is public) can have private fields, and the rest of
// PBS doesn't have to worry about the cookie data storage format.
type pbsCookieJson struct {
	LegacyUIDs map[string]string        `json:"uids,omitempty"`
	UIDs       map[string]uidWithExpiry `json:"tempUIDs,omitempty"`
	OptOut     bool                     `json:"optout,omitempty"`
	Birthday   *time.Time               `json:"bday,omitempty"`
}

func (deps *UserSyncDeps) GetUIDs(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	pc := usersync.ParsePBSCookieFromRequest(r, &deps.HostCookieSettings.OptOutCookie)
	pc.SetCookieOnResponse(w, deps.HostCookieSettings.Domain, deps.HostCookieSettings.TTL)
	json.NewEncoder(w).Encode(pc)
	return
}

// Struct for parsing json in google's response
type googleResponse struct {
	Success    bool
	ErrorCodes []string `json:"error-codes"`
}

func (deps *UserSyncDeps) VerifyRecaptcha(response string) error {
	ts := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: ssl.GetRootCAPool()},
	}

	client := &http.Client{
		Transport: ts,
	}
	resp, err := client.PostForm(RECAPTCHA_URL,
		url.Values{"secret": {deps.RecaptchaSecret}, "response": {response}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var gr = googleResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return err
	}
	if !gr.Success {
		return fmt.Errorf("Captcha verify failed: %s", strings.Join(gr.ErrorCodes, ", "))
	}
	return nil
}

func (deps *UserSyncDeps) OptOut(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	optout := r.FormValue("optout")
	rr := r.FormValue("g-recaptcha-response")

	if rr == "" {
		http.Redirect(w, r, fmt.Sprintf("%s/static/optout.html", deps.ExternalUrl), 301)
		return
	}

	err := deps.VerifyRecaptcha(rr)
	if err != nil {
		if glog.V(2) {
			glog.Infof("Opt Out failed recaptcha: %v", err)
		}
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	pc := usersync.ParsePBSCookieFromRequest(r, &deps.HostCookieSettings.OptOutCookie)
	pc.SetPreference(optout == "")

	pc.SetCookieOnResponse(w, deps.HostCookieSettings.Domain, deps.HostCookieSettings.TTL)
	if optout == "" {
		http.Redirect(w, r, deps.HostCookieSettings.OptInURL, 301)
	} else {
		http.Redirect(w, r, deps.HostCookieSettings.OptOutURL, 301)
	}
}
