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
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"howett.net/plist"
)

const (
	tssURL = "https://gs.apple.com/TSS/controller?action=2"
	// MobileDevice 1860.2.5.100.2 contains libauthinstall-1155.0.5.
	tssVersion       = "libauthinstall-1155.0.5"
	tssResponseLimit = 1 << 20
)

var tssHTTPClient = &http.Client{Timeout: time.Minute}

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
		return nil, fmt.Errorf("imagemounter: encode TSS request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, tssURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("imagemounter: %w", err)
	}
	httpRequest.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	httpRequest.Header.Set("Cache-Control", "no-cache")
	response, err := tssHTTPClient.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("imagemounter: TSS request: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, tssResponseLimit))
	if err != nil {
		return nil, fmt.Errorf("imagemounter: read TSS response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("imagemounter: TSS request: HTTP %s", response.Status)
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
				return nil, fmt.Errorf("imagemounter: TSS response status %q", value)
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
		return nil, errors.New("imagemounter: TSS response without REQUEST_STRING")
	}
	var response map[string]any
	if _, err := plist.Unmarshal([]byte(requestString), &response); err != nil {
		return nil, fmt.Errorf("imagemounter: decode TSS response: %w", err)
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
		return nil, errors.New("imagemounter: personalization identifiers without BoardId, ChipID or SecurityDomain")
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
	// go-ios uses production, secure and Image4 parameters for DDI tickets.
	parameters := map[string]any{
		"ApProductionMode": true,
		"ApSecurityMode":   true,
		"ApSupportsImg4":   true,
	}
	// pymobiledevice3 takes personalized DDI restore rules from
	// LoadableTrustCache.Info.RestoreRequestRules.
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

// The condition names follow pymobiledevice3 restore/tss.py; its action
// value 255 leaves an entry unchanged.
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
	// as hexadecimal strings.
	for _, key := range []string{"Cryptex1,ChipID", "Cryptex1,ProductClass"} {
		number, ok := numberValue(identity[key])
		if !ok {
			return nil, fmt.Errorf("imagemounter: the cryptex identity has no %s", key)
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
			return nil, fmt.Errorf("imagemounter: the cryptex identity has no %s", key)
		}
		request[key] = value
	}
	request["Cryptex1,Nonce"] = nonce
	// idevice accepts either a boolean or an unsigned integer for img4_chip_cpro.
	production, ok := boolValue(chip["img4_chip_cpro"])
	if !ok {
		return nil, errors.New("imagemounter: the chip instance has no img4_chip_cpro")
	}
	request["Cryptex1,ProductionMode"] = production
	chipID, chipOK := numberValue(chip["img4_chip_chip"])
	ecid, ecidOK := numberValue(chip["img4_chip_ecid"])
	if !chipOK || !ecidOK {
		return nil, errors.New("imagemounter: the chip instance has no img4_chip_chip or img4_chip_ecid")
	}
	// idevice encodes Cryptex1,UDID as the big-endian uint64 chip ID then ECID.
	udid := binary.BigEndian.AppendUint64(nil, chipID)
	request["Cryptex1,UDID"] = binary.BigEndian.AppendUint64(udid, ecid)
	request["Cryptex1,UniqueTagList"] = []byte{}
	// idevice sends only the Digest of Cryptex1 entries marked Personalize.
	// DDI build 27A9266e marks GenericDmg false and its three other assets true.
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
			return nil, fmt.Errorf("imagemounter: %s has no digest", key)
		}
		request[key] = map[string]any{"Digest": digest}
	}
	return request, nil
}
