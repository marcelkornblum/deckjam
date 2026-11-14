package server

import (
	"encoding/json"
	"fmt"
)

// GenerateHTTPShortcutsConfig builds an importable HTTP Shortcuts JSON configuration.
func GenerateHTTPShortcutsConfig(joinURL, guestID, guestName string) []byte {
	if guestID == "" {
		guestID = "guest-shortcut"
	}
	if guestName == "" {
		guestName = "Party Guest"
	}

	config := map[string]interface{}{
		"version":    53,
		"schema":     3,
		"categories": []interface{}{
			map[string]interface{}{
				"id":     "deckjam-category",
				"name":   "DeckJam",
				"layout": "grid",
				"shortcuts": []interface{}{
					"deckjam-share-shortcut",
				},
			},
		},
		"shortcuts": []interface{}{
			map[string]interface{}{
				"id":               "deckjam-share-shortcut",
				"name":             "Queue on DeckJam",
				"description":      "Share Spotify track directly into DeckJam queue",
				"executionType":    "app",
				"url":              fmt.Sprintf("%s/api/quick-share", joinURL),
				"method":           "POST",
				"bodyType":         "custom_text",
				"bodyContent":      fmt.Sprintf(`{"url":"{share_text}","guest_id":"%s","guest_name":"%s"}`, guestID, guestName),
				"contentType":      "application/json",
				"acceptShareTypes": []string{"text_plain"},
				"parameters": []interface{}{
					map[string]interface{}{
						"key":   "url",
						"value": "{share_text}",
					},
				},
				"responseHandling": map[string]interface{}{
					"uiType":  "toast",
					"success": "Track sent to DeckJam party queue!",
				},
			},
		},
	}

	bytes, _ := json.MarshalIndent(config, "", "  ")
	return bytes
}
