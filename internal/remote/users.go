package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	userSocialsBatchSize    = 50
	maximumUserSocialsUsers = maximumGuestCatalogSize
	maximumUsersPageBytes   = 8 << 20
)

// UserSocial is one social handle a user chose to show on their Partiful
// profile (observed 2026-10-02: getUsers returns socials as
// {"<platform>":{"value":"<handle>","visibility":"<scope>"}}; handles the
// caller may not see are absent rather than masked).
type UserSocial struct {
	Platform string
	Handle   string
}

type getUsersRequest struct {
	Data getUsersRequestData `json:"data"`
}

type getUsersRequestData struct {
	Params            getUsersParams `json:"params"`
	AmplitudeDeviceID string         `json:"amplitudeDeviceId"`
}

type getUsersParams struct {
	IDs []string `json:"ids"`
}

type getUsersResponse struct {
	Result *struct {
		Data []json.RawMessage `json:"data"`
	} `json:"result"`
}

// GetUserSocials returns the visible social handles for each requested user,
// keyed by user ID. Users with no visible handles map to an empty slice.
func (client Client) GetUserSocials(
	ctx context.Context,
	accessToken string,
	amplitudeDeviceID string,
	userIDs []string,
) (map[string][]UserSocial, error) {
	if len(userIDs) > maximumUserSocialsUsers {
		return nil, fmt.Errorf("%w: users traversal bound", ErrProtocolChanged)
	}
	socials := make(map[string][]UserSocial, len(userIDs))
	for start := 0; start < len(userIDs); start += userSocialsBatchSize {
		end := min(start+userSocialsBatchSize, len(userIDs))
		batch := userIDs[start:end]
		records, err := client.getUsersBatch(ctx, accessToken, amplitudeDeviceID, batch)
		if err != nil {
			return nil, err
		}
		for _, raw := range records {
			id, userSocials, err := decodeUserSocials(raw)
			if err != nil {
				return nil, fmt.Errorf("%w: user", ErrProtocolChanged)
			}
			socials[id] = userSocials
		}
	}
	for _, id := range userIDs {
		if _, ok := socials[id]; !ok {
			socials[id] = []UserSocial{}
		}
	}
	return socials, nil
}

func (client Client) getUsersBatch(
	ctx context.Context,
	accessToken string,
	amplitudeDeviceID string,
	userIDs []string,
) ([]json.RawMessage, error) {
	if client.HTTP == nil {
		return nil, fmt.Errorf("%w: users transport", ErrUnavailable)
	}
	payload, _ := json.Marshal(getUsersRequest{Data: getUsersRequestData{
		Params:            getUsersParams{IDs: userIDs},
		AmplitudeDeviceID: amplitudeDeviceID,
	}})
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		partifulCallableHost+"/getUsers",
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: users request", ErrUnavailable)
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.HTTP.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: users request failed", ErrUnavailable)
	}
	if response == nil || response.Body == nil {
		return nil, fmt.Errorf("%w: users response", ErrProtocolChanged)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: users status", ErrProtocolChanged)
	}
	if !eventJSONContentType(response.Header.Get("Content-Type")) {
		return nil, fmt.Errorf("%w: users content type", ErrProtocolChanged)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumUsersPageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: users response read", ErrUnavailable)
	}
	if len(body) > maximumUsersPageBytes || !utf8.Valid(body) {
		return nil, fmt.Errorf("%w: users response body", ErrProtocolChanged)
	}
	var page getUsersResponse
	if err := decodeSingleGuestJSON(body, &page); err != nil ||
		page.Result == nil ||
		page.Result.Data == nil ||
		len(page.Result.Data) > len(userIDs) {
		return nil, fmt.Errorf("%w: users response body", ErrProtocolChanged)
	}
	return page.Result.Data, nil
}

func decodeUserSocials(raw json.RawMessage) (string, []UserSocial, error) {
	object, err := decodeEventObject(raw)
	if err != nil {
		return "", nil, err
	}
	id, present, err := eventStringField(object, "id", false)
	if err != nil || !present || strings.TrimSpace(*id) == "" {
		return "", nil, fmt.Errorf("user id is invalid")
	}
	socials := []UserSocial{}
	rawSocials, ok := object["socials"]
	if !ok || bytes.Equal(bytes.TrimSpace(rawSocials), []byte("null")) {
		return *id, socials, nil
	}
	var platforms map[string]struct {
		Value *string `json:"value"`
	}
	if json.Unmarshal(rawSocials, &platforms) != nil {
		return "", nil, fmt.Errorf("user socials are invalid")
	}
	for platform, entry := range platforms {
		if strings.TrimSpace(platform) == "" {
			return "", nil, fmt.Errorf("user social platform is invalid")
		}
		if entry.Value == nil || strings.TrimSpace(*entry.Value) == "" {
			continue
		}
		socials = append(socials, UserSocial{
			Platform: platform,
			Handle:   strings.TrimSpace(*entry.Value),
		})
	}
	sort.Slice(socials, func(i, j int) bool { return socials[i].Platform < socials[j].Platform })
	return *id, socials, nil
}
