package api

import (
	"encoding/json"
	"net/http"

	"server/bonjour"
	"server/dlna"
	"server/rutor"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"

	sets "server/settings"
	"server/torr"
)

// Action: get, set, def
type setsReqJS struct {
	requestI
	// Kept raw so that a key the client left out can be told apart from one it
	// deliberately sent as false. See mergeBTSets.
	Sets json.RawMessage `json:"sets,omitempty"`
}

// mergeBTSets applies the fields present in raw on top of cur and returns the
// result, leaving anything the client did not send untouched.
//
// Decoding into a zero value instead would mean any client that does not know
// a field resets it on every save: an older app, or one built before the field
// existed, silently turns off everything it has never heard of.
func mergeBTSets(cur *sets.BTSets, raw json.RawMessage) (*sets.BTSets, error) {
	merged := sets.BTSets{}
	if cur != nil {
		merged = *cur
	}
	if len(raw) == 0 {
		return nil, errors.New("sets is empty")
	}
	if err := json.Unmarshal(raw, &merged); err != nil {
		return nil, err
	}
	return &merged, nil
}

// settings godoc
//
//	@Summary		Get / Set server settings
//	@Description	Allow to get or set server settings.
//
//	@Tags			API
//
//	@Param			request	body	setsReqJS	true	"Settings request. Available params for action: get, set, def"
//
//	@Accept			json
//	@Produce		json
//	@Security		BasicAuth
//	@Success		200	{object}	sets.BTSets	"Settings JSON or nothing. Depends on what action has been asked."
//	@Router			/settings [post]
func settings(c *gin.Context) {
	var req setsReqJS
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.AbortWithError(http.StatusBadRequest, err)
		return
	}

	if req.Action == "get" {
		c.JSON(200, sets.BTsets)
		return
	} else if req.Action == "set" {
		newSets, err := mergeBTSets(sets.BTsets, req.Sets)
		if err != nil {
			c.AbortWithError(http.StatusBadRequest, err)
			return
		}
		torr.SetSettings(newSets)
		dlna.Stop()
		if newSets.EnableDLNA {
			dlna.Start()
		}
		bonjour.Stop()
		if newSets.EnableBonjour {
			bonjour.Start()
		}
		rutor.Stop()
		rutor.Start()
		c.Status(200)
		return
	} else if req.Action == "def" {
		torr.SetDefSettings()
		dlna.Stop()
		bonjour.Stop()
		rutor.Stop()
		c.Status(200)
		return
	}
	c.AbortWithError(http.StatusBadRequest, errors.New("action is empty"))
}
