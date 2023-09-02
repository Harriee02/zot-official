//go:build mgmt
// +build mgmt

package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	guuid "github.com/gofrs/uuid"
	"github.com/project-zot/mockoidc"
	. "github.com/smartystreets/goconvey/convey"
	"gopkg.in/resty.v1"

	"zotregistry.io/zot/pkg/api"
	"zotregistry.io/zot/pkg/api/config"
	"zotregistry.io/zot/pkg/api/constants"
	extconf "zotregistry.io/zot/pkg/extensions/config"
	"zotregistry.io/zot/pkg/log"
	mTypes "zotregistry.io/zot/pkg/meta/types"
	reqCtx "zotregistry.io/zot/pkg/requestcontext"
	"zotregistry.io/zot/pkg/test"
	"zotregistry.io/zot/pkg/test/mocks"
)

var ErrUnexpectedError = errors.New("error: unexpected error")

type (
	apiKeyResponse struct {
		mTypes.APIKeyDetails
		APIKey string `json:"apiKey"`
	}
)

type (
	apiKeyListResponse struct {
		APIKeys []mTypes.APIKeyDetails `json:"apiKeys"`
	}
)

func TestAllowedMethodsHeaderAPIKey(t *testing.T) {
	defaultVal := true

	Convey("Test http options response", t, func() {
		conf := config.New()
		port := test.GetFreePort()
		conf.HTTP.Port = port
		conf.HTTP.Auth.APIKey = defaultVal
		baseURL := test.GetBaseURL(port)

		ctlr := api.NewController(conf)
		ctlr.Config.Storage.RootDirectory = t.TempDir()

		ctrlManager := test.NewControllerManager(ctlr)

		ctrlManager.StartAndWait(port)
		defer ctrlManager.StopServer()

		resp, _ := resty.R().Options(baseURL + constants.APIKeyPath)
		So(resp, ShouldNotBeNil)
		So(resp.Header().Get("Access-Control-Allow-Methods"), ShouldResemble, "GET,POST,DELETE,OPTIONS")
		So(resp.StatusCode(), ShouldEqual, http.StatusNoContent)
	})
}

func TestAPIKeys(t *testing.T) {
	Convey("Make a new controller", t, func() {
		port := test.GetFreePort()
		baseURL := test.GetBaseURL(port)

		conf := config.New()
		conf.HTTP.Port = port

		htpasswdPath := test.MakeHtpasswdFile()
		defer os.Remove(htpasswdPath)

		mockOIDCServer, err := test.MockOIDCRun()
		if err != nil {
			panic(err)
		}

		defer func() {
			err := mockOIDCServer.Shutdown()
			if err != nil {
				panic(err)
			}
		}()

		mockOIDCConfig := mockOIDCServer.Config()
		defaultVal := true

		conf.HTTP.Auth = &config.AuthConfig{
			HTPasswd: config.AuthHTPasswd{
				Path: htpasswdPath,
			},
			OpenID: &config.OpenIDConfig{
				Providers: map[string]config.OpenIDProviderConfig{
					"oidc": {
						ClientID:     mockOIDCConfig.ClientID,
						ClientSecret: mockOIDCConfig.ClientSecret,
						KeyPath:      "",
						Issuer:       mockOIDCConfig.Issuer,
						Scopes:       []string{"openid", "email", "groups"},
					},
				},
			},
			APIKey: defaultVal,
		}

		conf.HTTP.AccessControl = &config.AccessControlConfig{}

		conf.Extensions = &extconf.ExtensionConfig{}
		conf.Extensions.Search = &extconf.SearchConfig{}
		conf.Extensions.Search.Enable = &defaultVal
		conf.Extensions.Search.CVE = nil
		conf.Extensions.UI = &extconf.UIConfig{}
		conf.Extensions.UI.Enable = &defaultVal

		ctlr := api.NewController(conf)
		dir := t.TempDir()

		ctlr.Config.Storage.RootDirectory = dir

		cm := test.NewControllerManager(ctlr)

		cm.StartServer()
		defer cm.StopServer()
		test.WaitTillServerReady(baseURL)

		payload := api.APIKeyPayload{
			Label:  "test",
			Scopes: []string{"test"},
		}
		reqBody, err := json.Marshal(payload)
		So(err, ShouldBeNil)

		Convey("API key retrieved with basic auth", func() {
			resp, err := resty.R().
				SetBody(reqBody).
				SetBasicAuth("test", "test").
				Post(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusCreated)

			user := mockoidc.DefaultUser()

			// get API key and email from apikey route response
			var apiKeyResponse apiKeyResponse
			err = json.Unmarshal(resp.Body(), &apiKeyResponse)
			So(err, ShouldBeNil)

			email := user.Email
			So(email, ShouldNotBeEmpty)

			resp, err = resty.R().
				SetBasicAuth("test", apiKeyResponse.APIKey).
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			// get API key list with basic auth
			resp, err = resty.R().
				SetBasicAuth("test", "test").
				Get(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			var apiKeyListResponse apiKeyListResponse
			err = json.Unmarshal(resp.Body(), &apiKeyListResponse)
			So(err, ShouldBeNil)
			So(len(apiKeyListResponse.APIKeys), ShouldEqual, 1)
			So(apiKeyListResponse.APIKeys[0].CreatedAt, ShouldEqual, apiKeyResponse.APIKeyDetails.CreatedAt)
			So(apiKeyListResponse.APIKeys[0].CreatorUA, ShouldEqual, apiKeyResponse.APIKeyDetails.CreatorUA)
			So(apiKeyListResponse.APIKeys[0].Label, ShouldEqual, apiKeyResponse.APIKeyDetails.Label)
			So(apiKeyListResponse.APIKeys[0].Scopes, ShouldEqual, apiKeyResponse.APIKeyDetails.Scopes)
			So(apiKeyListResponse.APIKeys[0].UUID, ShouldEqual, apiKeyResponse.APIKeyDetails.UUID)

			// add another one
			resp, err = resty.R().
				SetBody(reqBody).
				SetBasicAuth("test", "test").
				Post(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusCreated)

			err = json.Unmarshal(resp.Body(), &apiKeyResponse)
			So(err, ShouldBeNil)

			resp, err = resty.R().
				SetBasicAuth("test", apiKeyResponse.APIKey).
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			// get API key list with api key auth
			resp, err = resty.R().
				SetBasicAuth("test", apiKeyResponse.APIKey).
				Get(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			err = json.Unmarshal(resp.Body(), &apiKeyListResponse)
			So(err, ShouldBeNil)
			So(len(apiKeyListResponse.APIKeys), ShouldEqual, 2)
		})

		Convey("API key retrieved with openID and with no expire", func() {
			client := resty.New()
			client.SetRedirectPolicy(test.CustomRedirectPolicy(20))

			// first login user
			resp, err := client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				SetQueryParam("provider", "oidc").
				Get(baseURL + constants.LoginPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)

			cookies := resp.Cookies()

			// call endpoint without session
			resp, err = client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				Post(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusUnauthorized)

			client.SetCookies(cookies)

			// call endpoint with session ( added to client after previous request)
			resp, err = client.R().
				SetBody(reqBody).
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				Post(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusCreated)

			user := mockoidc.DefaultUser()

			// get API key and email from apikey route response
			var apiKeyResponse apiKeyResponse
			err = json.Unmarshal(resp.Body(), &apiKeyResponse)
			So(err, ShouldBeNil)

			email := user.Email
			So(email, ShouldNotBeEmpty)

			// get API key list
			resp, err = client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				Get(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			var apiKeyListResponse apiKeyListResponse

			err = json.Unmarshal(resp.Body(), &apiKeyListResponse)
			So(err, ShouldBeNil)
			So(len(apiKeyListResponse.APIKeys), ShouldEqual, 1)
			So(apiKeyListResponse.APIKeys[0].CreatedAt, ShouldEqual, apiKeyResponse.APIKeyDetails.CreatedAt)
			So(apiKeyListResponse.APIKeys[0].CreatorUA, ShouldEqual, apiKeyResponse.APIKeyDetails.CreatorUA)
			So(apiKeyListResponse.APIKeys[0].Label, ShouldEqual, apiKeyResponse.APIKeyDetails.Label)
			So(apiKeyListResponse.APIKeys[0].Scopes, ShouldEqual, apiKeyResponse.APIKeyDetails.Scopes)
			So(apiKeyListResponse.APIKeys[0].UUID, ShouldEqual, apiKeyResponse.APIKeyDetails.UUID)

			resp, err = client.R().
				SetBasicAuth(email, apiKeyResponse.APIKey).
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			// trigger errors
			ctlr.MetaDB = mocks.MetaDBMock{
				GetUserAPIKeyInfoFn: func(hashedKey string) (string, error) {
					return "", ErrUnexpectedError
				},
			}

			resp, err = client.R().
				SetBasicAuth(email, apiKeyResponse.APIKey).
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusInternalServerError)

			ctlr.MetaDB = mocks.MetaDBMock{
				GetUserAPIKeyInfoFn: func(hashedKey string) (string, error) {
					return user.Email, nil
				},
				GetUserGroupsFn: func(ctx context.Context) ([]string, error) {
					return []string{}, ErrUnexpectedError
				},
			}

			resp, err = client.R().
				SetBasicAuth(email, apiKeyResponse.APIKey).
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusInternalServerError)

			ctlr.MetaDB = mocks.MetaDBMock{
				GetUserAPIKeyInfoFn: func(hashedKey string) (string, error) {
					return user.Email, nil
				},
				UpdateUserAPIKeyLastUsedFn: func(ctx context.Context, hashedKey string) error {
					return ErrUnexpectedError
				},
			}

			resp, err = client.R().
				SetBasicAuth(email, apiKeyResponse.APIKey).
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusInternalServerError)

			client = resty.New()

			// call endpoint without session
			resp, err = client.R().
				SetBody(reqBody).
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				Post(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusUnauthorized)
		})

		Convey("API key retrieved with openID and with long expire", func() {
			payload := api.APIKeyPayload{
				Label:          "test",
				Scopes:         []string{"test"},
				ExpirationDate: time.Now().Add(time.Hour).Local().Format(constants.APIKeyTimeFormat),
			}

			reqBody, err := json.Marshal(payload)
			So(err, ShouldBeNil)

			client := resty.New()

			// mgmt should work both unauthenticated and authenticated
			resp, err := client.R().Get(baseURL + constants.FullMgmt)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			client.SetRedirectPolicy(test.CustomRedirectPolicy(20))
			// first login user
			resp, err = client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				SetQueryParam("provider", "oidc").
				Get(baseURL + constants.LoginPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusCreated)

			client.SetCookies(resp.Cookies())

			// call endpoint with session ( added to client after previous request)
			resp, err = client.R().
				SetBody(reqBody).
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				Post(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusCreated)

			var apiKeyResponse apiKeyResponse
			err = json.Unmarshal(resp.Body(), &apiKeyResponse)
			So(err, ShouldBeNil)

			// get API key list
			resp, err = client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				Get(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			var apiKeyListResponse apiKeyListResponse

			err = json.Unmarshal(resp.Body(), &apiKeyListResponse)
			So(err, ShouldBeNil)
			So(len(apiKeyListResponse.APIKeys), ShouldEqual, 1)
			So(apiKeyListResponse.APIKeys[0].CreatedAt, ShouldEqual, apiKeyResponse.APIKeyDetails.CreatedAt)
			So(apiKeyListResponse.APIKeys[0].CreatorUA, ShouldEqual, apiKeyResponse.APIKeyDetails.CreatorUA)
			So(apiKeyListResponse.APIKeys[0].Label, ShouldEqual, apiKeyResponse.APIKeyDetails.Label)
			So(apiKeyListResponse.APIKeys[0].Scopes, ShouldEqual, apiKeyResponse.APIKeyDetails.Scopes)
			So(apiKeyListResponse.APIKeys[0].UUID, ShouldEqual, apiKeyResponse.APIKeyDetails.UUID)

			user := mockoidc.DefaultUser()
			email := user.Email
			So(email, ShouldNotBeEmpty)

			resp, err = client.R().
				SetBasicAuth(email, apiKeyResponse.APIKey).
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			// auth with API key
			// we need new client without session cookie set
			client = resty.New()
			client.SetRedirectPolicy(test.CustomRedirectPolicy(20))

			resp, err = client.R().
				SetBasicAuth(email, apiKeyResponse.APIKey).
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			resp, err = client.R().
				SetBasicAuth(email, apiKeyResponse.APIKey).
				Get(baseURL + constants.FullMgmt)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			// get API key list
			resp, err = resty.R().
				SetBasicAuth(email, apiKeyResponse.APIKey).
				Get(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			err = json.Unmarshal(resp.Body(), &apiKeyListResponse)
			So(err, ShouldBeNil)
			So(len(apiKeyListResponse.APIKeys), ShouldEqual, 1)

			// invalid api keys
			resp, err = client.R().
				SetBasicAuth("invalidEmail", apiKeyResponse.APIKey).
				Get(baseURL + constants.FullMgmt)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusUnauthorized)

			resp, err = client.R().
				SetBasicAuth(email, "noprefixAPIKey").
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusUnauthorized)

			resp, err = client.R().
				SetBasicAuth(email, "zak_notworkingAPIKey").
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusUnauthorized)

			userAc := reqCtx.NewUserAccessControl()
			userAc.SetUsername(email)
			ctx := userAc.DeriveContext(context.Background())

			err = ctlr.MetaDB.DeleteUserData(ctx)
			So(err, ShouldBeNil)

			resp, err = client.R().
				SetBasicAuth(email, apiKeyResponse.APIKey).
				Get(baseURL + constants.FullMgmt)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusInternalServerError)

			client = resty.New()
			client.SetRedirectPolicy(test.CustomRedirectPolicy(20))

			// without creds should work
			resp, err = client.R().Get(baseURL + constants.FullMgmt)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			// login again
			resp, err = client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				SetQueryParam("provider", "oidc").
				Get(baseURL + constants.LoginPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusCreated)

			client.SetCookies(resp.Cookies())

			resp, err = client.R().
				SetBody(reqBody).
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				Post(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusCreated)

			err = json.Unmarshal(resp.Body(), &apiKeyResponse)
			So(err, ShouldBeNil)

			// should work with session
			resp, err = client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				Get(baseURL + constants.FullMgmt)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			resp, err = client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			// should work with api key
			resp, err = client.R().
				SetBasicAuth(email, apiKeyResponse.APIKey).
				Get(baseURL + constants.FullMgmt)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			resp, err = client.R().
				SetBasicAuth(email, apiKeyResponse.APIKey).
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			err = json.Unmarshal(resp.Body(), &apiKeyResponse)
			So(err, ShouldBeNil)

			// delete api key
			resp, err = client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				SetQueryParam("id", apiKeyResponse.UUID).
				Delete(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			// apiKey removed, should get 401
			resp, err = client.R().
				SetBasicAuth(email, apiKeyResponse.APIKey).
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusUnauthorized)

			resp, err = client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				Delete(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusBadRequest)

			resp, err = client.R().
				SetBasicAuth(email, apiKeyResponse.APIKey).
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusUnauthorized)

			resp, err = client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			// get API key list
			resp, err = client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				Get(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			err = json.Unmarshal(resp.Body(), &apiKeyListResponse)
			So(err, ShouldBeNil)
			So(len(apiKeyListResponse.APIKeys), ShouldEqual, 0)

			resp, err = client.R().
				SetBasicAuth("test", "test").
				SetQueryParam("id", apiKeyResponse.UUID).
				Delete(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			// unsupported method
			resp, err = client.R().
				Put(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusMethodNotAllowed)
		})

		Convey("API key retrieved with openID and with short expire", func() {
			expirationDate := time.Now().Add(1 * time.Second).Local().Round(time.Second)
			payload := api.APIKeyPayload{
				Label:          "test",
				Scopes:         []string{"test"},
				ExpirationDate: expirationDate.Format(constants.APIKeyTimeFormat),
			}

			reqBody, err := json.Marshal(payload)
			So(err, ShouldBeNil)

			client := resty.New()

			client.SetRedirectPolicy(test.CustomRedirectPolicy(20))
			// first login user
			resp, err := client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				SetQueryParam("provider", "oidc").
				Get(baseURL + constants.LoginPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusCreated)

			client.SetCookies(resp.Cookies())

			// call endpoint with session (added to client after previous request)
			resp, err = client.R().
				SetBody(reqBody).
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				Post(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusCreated)

			var apiKeyResponse apiKeyResponse
			err = json.Unmarshal(resp.Body(), &apiKeyResponse)
			So(err, ShouldBeNil)

			user := mockoidc.DefaultUser()
			email := user.Email
			So(email, ShouldNotBeEmpty)

			// get API key list
			resp, err = client.R().
				SetBasicAuth(email, apiKeyResponse.APIKey).
				Get(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			var apiKeyListResponse apiKeyListResponse

			err = json.Unmarshal(resp.Body(), &apiKeyListResponse)
			So(err, ShouldBeNil)
			So(len(apiKeyListResponse.APIKeys), ShouldEqual, 1)
			So(apiKeyListResponse.APIKeys[0].CreatedAt, ShouldEqual, apiKeyResponse.APIKeyDetails.CreatedAt)
			So(apiKeyListResponse.APIKeys[0].CreatorUA, ShouldEqual, apiKeyResponse.APIKeyDetails.CreatorUA)
			So(apiKeyListResponse.APIKeys[0].Label, ShouldEqual, apiKeyResponse.APIKeyDetails.Label)
			So(apiKeyListResponse.APIKeys[0].Scopes, ShouldEqual, apiKeyResponse.APIKeyDetails.Scopes)
			So(apiKeyListResponse.APIKeys[0].UUID, ShouldEqual, apiKeyResponse.APIKeyDetails.UUID)
			So(apiKeyListResponse.APIKeys[0].IsExpired, ShouldEqual, false)
			So(apiKeyListResponse.APIKeys[0].ExpirationDate.Equal(expirationDate), ShouldBeTrue)

			resp, err = client.R().
				SetBasicAuth(email, apiKeyResponse.APIKey).
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			// sleep past expire time
			time.Sleep(1500 * time.Millisecond)

			resp, err = client.R().
				SetBasicAuth(email, apiKeyResponse.APIKey).
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusUnauthorized)

			// again for coverage
			resp, err = client.R().
				SetBasicAuth(email, apiKeyResponse.APIKey).
				Get(baseURL + "/v2/_catalog")
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusUnauthorized)

			// get API key list with session authn
			resp, err = client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				Get(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			err = json.Unmarshal(resp.Body(), &apiKeyListResponse)
			So(err, ShouldBeNil)
			So(len(apiKeyListResponse.APIKeys), ShouldEqual, 1)
			So(apiKeyListResponse.APIKeys[0].CreatedAt, ShouldEqual, apiKeyResponse.APIKeyDetails.CreatedAt)
			So(apiKeyListResponse.APIKeys[0].CreatorUA, ShouldEqual, apiKeyResponse.APIKeyDetails.CreatorUA)
			So(apiKeyListResponse.APIKeys[0].Label, ShouldEqual, apiKeyResponse.APIKeyDetails.Label)
			So(apiKeyListResponse.APIKeys[0].Scopes, ShouldEqual, apiKeyResponse.APIKeyDetails.Scopes)
			So(apiKeyListResponse.APIKeys[0].UUID, ShouldEqual, apiKeyResponse.APIKeyDetails.UUID)
			So(apiKeyListResponse.APIKeys[0].IsExpired, ShouldEqual, true)
			So(apiKeyListResponse.APIKeys[0].ExpirationDate.Equal(expirationDate), ShouldBeTrue)

			// delete expired api key
			resp, err = client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				SetQueryParam("id", apiKeyResponse.UUID).
				Delete(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			// get API key list with session authn
			resp, err = client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				Get(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusOK)

			err = json.Unmarshal(resp.Body(), &apiKeyListResponse)
			So(err, ShouldBeNil)
			So(len(apiKeyListResponse.APIKeys), ShouldEqual, 0)
		})

		Convey("Create API key with expirationDate before actual date", func() {
			expirationDate := time.Now().Add(-5 * time.Second).Local().Round(time.Second)
			payload := api.APIKeyPayload{
				Label:          "test",
				Scopes:         []string{"test"},
				ExpirationDate: expirationDate.Format(constants.APIKeyTimeFormat),
			}

			reqBody, err := json.Marshal(payload)
			So(err, ShouldBeNil)

			client := resty.New()

			client.SetRedirectPolicy(test.CustomRedirectPolicy(20))
			// first login user
			resp, err := client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				SetQueryParam("provider", "oidc").
				Get(baseURL + constants.LoginPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusCreated)

			client.SetCookies(resp.Cookies())

			// call endpoint with session ( added to client after previous request)
			resp, err = client.R().
				SetBody(reqBody).
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				Post(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusBadRequest)
		})

		Convey("Create API key with unparsable expirationDate", func() {
			expirationDate := time.Now().Add(-5 * time.Second).Local().Round(time.Second)
			payload := api.APIKeyPayload{
				Label:          "test",
				Scopes:         []string{"test"},
				ExpirationDate: expirationDate.Format(time.RFC1123Z),
			}

			reqBody, err := json.Marshal(payload)
			So(err, ShouldBeNil)

			client := resty.New()

			client.SetRedirectPolicy(test.CustomRedirectPolicy(20))
			// first login user
			resp, err := client.R().
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				SetQueryParam("provider", "oidc").
				Get(baseURL + constants.LoginPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusCreated)

			client.SetCookies(resp.Cookies())

			// call endpoint with session ( added to client after previous request)
			resp, err = client.R().
				SetBody(reqBody).
				SetHeader(constants.SessionClientHeaderName, constants.SessionClientHeaderValue).
				Post(baseURL + constants.APIKeyPath)
			So(err, ShouldBeNil)
			So(resp, ShouldNotBeNil)
			So(resp.StatusCode(), ShouldEqual, http.StatusBadRequest)
		})

		Convey("Test error handling when API Key handler reads the request body", func() {
			request, _ := http.NewRequestWithContext(context.TODO(),
				http.MethodPost, "baseURL", errReader(0))
			response := httptest.NewRecorder()

			rthdlr := api.NewRouteHandler(ctlr)
			rthdlr.CreateAPIKey(response, request)

			resp := response.Result()
			defer resp.Body.Close()
			So(resp.StatusCode, ShouldEqual, http.StatusInternalServerError)
		})
	})
}

func TestAPIKeysOpenDBError(t *testing.T) {
	Convey("Test API keys - unable to create database", t, func() {
		conf := config.New()
		htpasswdPath := test.MakeHtpasswdFile()
		defer os.Remove(htpasswdPath)

		mockOIDCServer, err := test.MockOIDCRun()
		if err != nil {
			panic(err)
		}

		defer func() {
			err := mockOIDCServer.Shutdown()
			if err != nil {
				panic(err)
			}
		}()

		mockOIDCConfig := mockOIDCServer.Config()
		defaultVal := true

		conf.HTTP.Auth = &config.AuthConfig{
			HTPasswd: config.AuthHTPasswd{
				Path: htpasswdPath,
			},

			OpenID: &config.OpenIDConfig{
				Providers: map[string]config.OpenIDProviderConfig{
					"oidc": {
						ClientID:     mockOIDCConfig.ClientID,
						ClientSecret: mockOIDCConfig.ClientSecret,
						KeyPath:      "",
						Issuer:       mockOIDCConfig.Issuer,
						Scopes:       []string{"openid", "email"},
					},
				},
			},

			APIKey: defaultVal,
		}

		ctlr := api.NewController(conf)
		dir := t.TempDir()

		err = os.Chmod(dir, 0o000)
		So(err, ShouldBeNil)

		ctlr.Config.Storage.RootDirectory = dir
		cm := test.NewControllerManager(ctlr)

		So(func() {
			cm.StartServer()
		}, ShouldPanic)
	})
}

func TestAPIKeysGeneratorErrors(t *testing.T) {
	Convey("Test API keys - unable to generate API keys and API Key IDs", t, func() {
		log := log.NewLogger("debug", "")

		apiKey, apiKeyID, err := api.GenerateAPIKey(guuid.DefaultGenerator, log)
		So(err, ShouldBeNil)
		So(apiKey, ShouldNotEqual, "")
		So(apiKeyID, ShouldNotEqual, "")

		generator := &mockUUIDGenerator{
			guuid.DefaultGenerator, 0, 0,
		}

		apiKey, apiKeyID, err = api.GenerateAPIKey(generator, log)
		So(err, ShouldNotBeNil)
		So(apiKey, ShouldEqual, "")
		So(apiKeyID, ShouldEqual, "")

		generator = &mockUUIDGenerator{
			guuid.DefaultGenerator, 1, 0,
		}

		apiKey, apiKeyID, err = api.GenerateAPIKey(generator, log)
		So(err, ShouldNotBeNil)
		So(apiKey, ShouldEqual, "")
		So(apiKeyID, ShouldEqual, "")
	})
}

type mockUUIDGenerator struct {
	guuid.Generator
	succeedAttempts int
	attemptCount    int
}

func (gen *mockUUIDGenerator) NewV4() (
	guuid.UUID, error,
) {
	defer func() {
		gen.attemptCount += 1
	}()

	if gen.attemptCount >= gen.succeedAttempts {
		return guuid.UUID{}, ErrUnexpectedError
	}

	return guuid.DefaultGenerator.NewV4()
}

type errReader int

func (errReader) Read(p []byte) (int, error) {
	return 0, fmt.Errorf("test error") //nolint:goerr113
}