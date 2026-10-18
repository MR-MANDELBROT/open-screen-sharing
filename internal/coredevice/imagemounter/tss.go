// The TSS requests follow go-ios ios/imagemounter/tss.go
// (https://github.com/danielpaulus/go-ios, MIT License, Copyright (c) 2019
// danielpaulus) for the personalized image, and idevice
// idevice/src/tss.rs (https://github.com/jkcoxson/idevice, MIT License,
// Copyright 2026 Jackson Coxson) for the cryptex.

package imagemounter

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	E "github.com/sagernet/sing/common/exceptions"
	"howett.net/plist"
)

const (
	tssURL = "https://gs.apple.com/TSS/controller?action=2"
	// MobileDevice sets @VersionInfo to libauthinstall-1155.0.5 in its TSS
	// requests (MobileDevice 1860.2.5.100.2, macOS 27.0.1 26A434, UUID
	// E8CAD825-D005-3AEE-99F3-2E42DA343A0E,
	// _AMAuthInstallRequestSendSyncWithHeader 0x295a6c..0x295a84,
	// _AMAuthInstallSupportGetLibraryVersionString 0x19e3e0..0x19e3e8).
	tssVersion       = "libauthinstall-1155.0.5"
	tssResponseLimit = 1 << 20
)

type TSSError struct {
	Status  int
	Message string
}

func (e *TSSError) Error() string {
	return fmt.Sprintf("imagemounter: Apple's signing server refused: status %d: %s", e.Status, e.Message)
}

func sendTSS(ctx context.Context, request map[string]any) (map[string]any, error) {
	body, err := plist.MarshalIndent(request, plist.XMLFormat, "\t")
	if err != nil {
		return nil, E.Cause(err, "imagemounter: encode TSS request")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, tssURL, bytes.NewReader(body))
	if err != nil {
		return nil, E.Cause(err, "imagemounter")
	}
	httpRequest.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	httpRequest.Header.Set("Cache-Control", "no-cache")
	response, err := httpClient.Do(httpRequest)
	if err != nil {
		return nil, E.Cause(err, "imagemounter: send TSS request")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, tssResponseLimit))
	if err != nil {
		return nil, E.Cause(err, "imagemounter: read TSS response")
	}
	if response.StatusCode != http.StatusOK {
		return nil, E.New("imagemounter: TSS request: HTTP ", response.Status)
	}
	return parseTSSResponse(data)
}

func parseTSSResponse(data []byte) (map[string]any, error) {
	head, requestString, hasRequest := strings.Cut(string(data), "REQUEST_STRING=")
	status := -1
	var message string
	for field := range strings.SplitSeq(head, "&") {
		key, value, _ := strings.Cut(field, "=")
		switch key {
		case "STATUS":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return nil, E.New("imagemounter: TSS response status ", strconv.Quote(value))
			}
			status = parsed
		case "MESSAGE":
			message = value
		}
	}
	if status != 0 {
		return nil, &TSSError{Status: status, Message: message}
	}
	if !hasRequest {
		return nil, E.New("imagemounter: TSS response without REQUEST_STRING")
	}
	var response map[string]any
	if _, err := plist.Unmarshal([]byte(requestString), &response); err != nil {
		return nil, E.Cause(err, "imagemounter: decode TSS response")
	}
	return response, nil
}

func newTSSRequest() map[string]any {
	return map[string]any{
		"@HostPlatformInfo": "mac",
		"@VersionInfo":      tssVersion,
		"@UUID":             strings.ToUpper(uuid.NewString()),
	}
}

func personalizedRequest(identity buildIdentity, identifiers map[string]any, ecid uint64, nonce []byte) (map[string]any, error) {
	board, boardOK := numberValue(identifiers["BoardId"])
	chip, chipOK := numberValue(identifiers["ChipID"])
	securityDomain, domainOK := numberValue(identifiers["SecurityDomain"])
	if !boardOK || !chipOK || !domainOK {
		return nil, E.New("imagemounter: personalization identifiers without BoardId, ChipID or SecurityDomain")
	}
	request := newTSSRequest()
	maps.Copy(request, map[string]any{
		"@ApImg4Ticket":    true,
		"@BBTicket":        true,
		"ApBoardID":        board,
		"ApChipID":         chip,
		"ApECID":           ecid,
		"ApNonce":          nonce,
		"ApProductionMode": true,
		"ApSecurityDomain": securityDomain,
		"ApSecurityMode":   true,
		"SepNonce":         make([]byte, 20),
		"UID_MODE":         false,
	})
	for key, value := range identifiers {
		if strings.HasPrefix(key, "Ap,") {
			request[key] = value
		}
	}
	// go-ios uses production, secure and Image4 parameters for DDI tickets
	// (go-ios ca2c56d, ios/imagemounter/tss.go:55-59).
	parameters := map[string]any{
		"ApProductionMode": true,
		"ApSecurityMode":   true,
		"ApSupportsImg4":   true,
	}
	// pymobiledevice3 takes personalized DDI restore rules from
	// LoadableTrustCache.Info.RestoreRequestRules (pymobiledevice3 9c33842,
	// pymobiledevice3/services/mobile_image_mounter.py:516-517).
	var rules []any
	if trustCache, ok := identity.entry("LoadableTrustCache"); ok {
		info, _ := trustCache["Info"].(map[string]any)
		rules, _ = info["RestoreRequestRules"].([]any)
	}
	for key, value := range identity.manifestEntries() {
		entry, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if trusted, _ := entry["Trusted"].(bool); !trusted {
			continue
		}
		requestEntry := make(map[string]any, len(entry))
		for entryKey, entryValue := range entry {
			if entryKey != "Info" {
				requestEntry[entryKey] = entryValue
			}
		}
		if _, ok := requestEntry["Digest"]; !ok {
			requestEntry["Digest"] = []byte{}
		}
		applyRestoreRequestRules(requestEntry, parameters, rules)
		request[key] = requestEntry
	}
	return request, nil
}

// pymobiledevice3 maps these condition names to the same parameters, and its
// action value 255 leaves an entry unchanged (pymobiledevice3 9c33842,
// pymobiledevice3/restore/tss.py:118-155).
func applyRestoreRequestRules(entry, parameters map[string]any, rules []any) {
	for _, value := range rules {
		rule, _ := value.(map[string]any)
		conditions, _ := rule["Conditions"].(map[string]any)
		if !restoreRuleMatches(conditions, parameters) {
			continue
		}
		actions, _ := rule["Actions"].(map[string]any)
		for key, action := range actions {
			if number, ok := action.(uint64); ok && number == 255 {
				continue
			}
			entry[key] = action
		}
	}
}

func restoreRuleMatches(conditions, parameters map[string]any) bool {
	for key, want := range conditions {
		var parameter string
		switch key {
		case "ApRawProductionMode", "ApCurrentProductionMode":
			parameter = "ApProductionMode"
		case "ApRawSecurityMode":
			parameter = "ApSecurityMode"
		case "ApRequiresImage4":
			parameter = "ApSupportsImg4"
		default:
			return false
		}
		got, ok := parameters[parameter]
		if !ok || got != want {
			return false
		}
	}
	return true
}

func cryptexRequest(identity buildIdentity, chip map[string]any, nonce []byte) (map[string]any, error) {
	request := newTSSRequest()
	request["@Cryptex1,Ticket"] = true
	// DDI build 27A9266e encodes Cryptex1,ChipID and Cryptex1,ProductClass
	// as hexadecimal strings (BuildManifest.plist, DDI 27A9266e).
	for _, key := range []string{"Cryptex1,ChipID", "Cryptex1,ProductClass"} {
		number, ok := numberValue(identity[key])
		if !ok {
			return nil, E.New("imagemounter: the cryptex identity has no ", key)
		}
		request[key] = number
	}
	for _, key := range []string{
		"Cryptex1,Type",
		"Cryptex1,SubType",
		"Cryptex1,UseProductClass",
		"Cryptex1,NonceDomain",
		"Cryptex1,Version",
		"Cryptex1,PreauthorizationVersion",
	} {
		value, ok := identity[key]
		if !ok {
			return nil, E.New("imagemounter: the cryptex identity has no ", key)
		}
		request[key] = value
	}
	request["Cryptex1,Nonce"] = nonce
	// idevice accepts either a boolean or an unsigned integer for img4_chip_cpro
	// (idevice 3854a5d, idevice/src/tss.rs:200-214).
	production, ok := boolValue(chip["img4_chip_cpro"])
	if !ok {
		return nil, E.New("imagemounter: the chip instance has no img4_chip_cpro")
	}
	request["Cryptex1,ProductionMode"] = production
	chipID, chipOK := numberValue(chip["img4_chip_chip"])
	ecid, ecidOK := numberValue(chip["img4_chip_ecid"])
	if !chipOK || !ecidOK {
		return nil, E.New("imagemounter: the chip instance has no img4_chip_chip or img4_chip_ecid")
	}
	// idevice encodes Cryptex1,UDID as the big-endian uint64 chip ID then ECID
	// (idevice 3854a5d, idevice/src/tss.rs:215,720-737).
	udid := binary.BigEndian.AppendUint64(nil, chipID)
	request["Cryptex1,UDID"] = binary.BigEndian.AppendUint64(udid, ecid)
	request["Cryptex1,UniqueTagList"] = []byte{}
	// idevice sends only the Digest of Cryptex1 entries marked Personalize
	// (idevice 3854a5d, idevice/src/tss.rs:224-246). DDI build 27A9266e marks
	// GenericDmg false and its three other assets true (BuildManifest.plist,
	// DDI 27A9266e).
	for key, value := range identity.manifestEntries() {
		entry, ok := value.(map[string]any)
		if !ok || !strings.HasPrefix(key, "Cryptex1,") {
			continue
		}
		info, _ := entry["Info"].(map[string]any)
		if personalize, _ := info["Personalize"].(bool); !personalize {
			continue
		}
		digest, ok := entry["Digest"].([]byte)
		if !ok {
			return nil, E.New("imagemounter: ", key, " has no digest")
		}
		request[key] = map[string]any{"Digest": digest}
	}
	return request, nil
}
