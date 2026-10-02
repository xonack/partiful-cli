package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KalebCole/partiful-cli/internal/app"
)

type guestSocialOutput struct {
	Platform string `json:"platform"`
	Handle   string `json:"handle"`
}

type guestWithSocialsOutput struct {
	DisplayName string               `json:"displayName"`
	Cohost      bool                 `json:"cohost"`
	Socials     *[]guestSocialOutput `json:"socials"`
}

// Guest documents link accounts through a "user" reference (observed
// 2026-10-02); getUsers returns visible handles keyed by platform.
const socialsGuestDocuments = `{"documents":[` +
	`{"name":"projects/getpartiful/databases/(default)/documents/events/event-example/guests/private-guest-1","fields":{"name":{"stringValue":"Alice Example"},"status":{"stringValue":"GOING"},"count":{"integerValue":"1"},"user":{"referenceValue":"projects/getpartiful/databases/(default)/documents/users/private-attendee"}}},` +
	`{"name":"projects/getpartiful/databases/(default)/documents/events/event-example/guests/private-guest-2","fields":{"name":{"stringValue":"Cohost Example"},"status":{"stringValue":"GOING"},"count":{"integerValue":"1"},"user":{"referenceValue":"projects/getpartiful/databases/(default)/documents/users/private-cohost"}}},` +
	`{"name":"projects/getpartiful/databases/(default)/documents/events/event-example/guests/private-guest-3","fields":{"name":{"stringValue":"No Account"},"status":{"stringValue":"GOING"},"count":{"integerValue":"1"}}}` +
	`]}`

const socialsUsersBody = `{"result":{"data":[` +
	`{"id":"private-attendee","name":"Alice Example","socials":{"twitter":{"visibility":"MUTUALS","value":"alice_x"},"instagram":{"visibility":"MUTUALS","value":"alice.ig"}}},` +
	`{"id":"private-cohost","name":"Cohost Example","_tags":[]}` +
	`]}}`

func socialsDependencies(t *testing.T, usersBody string, calls *int) app.Dependencies {
	t.Helper()
	const credentials = `{"accessToken":"private-access-token","userId":"private-host","expiresAt":"2026-08-12T02:00:00Z"}`
	return withTestCursorCrypto(app.Dependencies{
		Files: fakeFilesystem{
			readFile: func(string) ([]byte, error) { return []byte(credentials), nil },
		},
		CredentialsPath: "/config/partiful/credentials.json",
		Now: func() time.Time {
			return time.Date(2026, time.August, 12, 0, 0, 0, 0, time.UTC)
		},
		AuthRandom: strings.NewReader(strings.Repeat("0123456789abcdef", 4)),
		HTTP: scriptedHTTP{do: func(request *http.Request) (*http.Response, error) {
			*calls++
			switch *calls {
			case 1:
				return jsonResponse(http.StatusOK, `{"result":{"data":{"event":{"id":"event-example","ownerIds":["private-host","private-cohost"],"rsvpsEnabled":true,"atCapacity":false,"plusOneNamesRequired":false,"questionnaireVersions":null,"hasGuests":true}}}}`), nil
			case 2:
				return jsonResponse(http.StatusBadRequest, `{"error":{"message":"User is host","status":"FAILED_PRECONDITION"}}`), nil
			case 3:
				return jsonResponse(http.StatusOK, socialsGuestDocuments), nil
			case 4:
				if request.URL.String() != "https://api.partiful.com/getUsers" {
					t.Fatalf("request 4 = %s, want getUsers", request.URL)
				}
				body, _ := io.ReadAll(request.Body)
				var payload struct {
					Data struct {
						Params struct {
							IDs []string `json:"ids"`
						} `json:"params"`
					} `json:"data"`
				}
				if err := json.Unmarshal(body, &payload); err != nil ||
					!reflect.DeepEqual(payload.Data.Params.IDs, []string{"private-attendee", "private-cohost"}) {
					t.Fatalf("getUsers body = %s, want both linked account ids", body)
				}
				return jsonResponse(http.StatusOK, usersBody), nil
			default:
				return nil, errors.New("unexpected request")
			}
		}},
	})
}

func TestExecuteGuestsListSocialsAttachesVisibleHandles(t *testing.T) {
	calls := 0
	result := app.Execute(context.Background(), app.Request{
		Argv: []string{"guests", "list", "event-example", "--socials"},
	}, socialsDependencies(t, socialsUsersBody, &calls))
	if result.ExitCode != 0 || result.Stderr != "" {
		t.Fatalf("result = %#v, want guest list with socials", result)
	}
	var envelope struct {
		Data struct {
			Items []guestWithSocialsOutput `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &envelope); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	none := []guestSocialOutput{}
	alice := []guestSocialOutput{{Platform: "instagram", Handle: "alice.ig"}, {Platform: "twitter", Handle: "alice_x"}}
	want := []guestWithSocialsOutput{
		{DisplayName: "Alice Example", Cohost: false, Socials: &alice},
		{DisplayName: "Cohost Example", Cohost: true, Socials: &none},
		{DisplayName: "No Account", Cohost: false, Socials: &none},
	}
	if !reflect.DeepEqual(envelope.Data.Items, want) {
		t.Fatalf("items = %s, want %#v", result.Stdout, want)
	}
	for _, privateValue := range []string{"private-attendee", "private-cohost", "private-guest-1", "MUTUALS"} {
		if strings.Contains(result.Stdout, privateValue) {
			t.Fatalf("output exposed private value %q", privateValue)
		}
	}
	if calls != 4 {
		t.Fatalf("request count = %d, want event, getGuests, subcollection, getUsers", calls)
	}
}

func TestExecuteGuestsListWithoutSocialsSkipsUserLookup(t *testing.T) {
	calls := 0
	result := app.Execute(context.Background(), app.Request{
		Argv: []string{"guests", "list", "event-example"},
	}, socialsDependencies(t, socialsUsersBody, &calls))
	if result.ExitCode != 0 || strings.Contains(result.Stdout, "socials") {
		t.Fatalf("result = %#v, want guest list without socials", result)
	}
	if calls != 3 {
		t.Fatalf("request count = %d, want no getUsers call", calls)
	}
}

func TestExecuteGuestsListSocialsFailsClosedOnUnknownUserShape(t *testing.T) {
	calls := 0
	result := app.Execute(context.Background(), app.Request{
		Argv: []string{"guests", "list", "event-example", "--socials"},
	}, socialsDependencies(t, `{"result":{"data":[{"name":"Alice Example"}]}}`, &calls))
	if result.ExitCode == 0 || !strings.Contains(result.Stdout, "GUESTS_PROTOCOL_CHANGED") {
		t.Fatalf("result = %#v, want fail-closed protocol change", result)
	}
}
