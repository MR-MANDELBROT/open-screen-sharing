package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sagernet/asc-go/asc"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	F "github.com/sagernet/sing/common/format"
	"howett.net/plist"
)

const (
	appID             = "6818992574"
	groupID           = "c0f4368a-6283-4c47-86a3-aec179465ec2"
	bundleID          = "io.nekohasekai.screensharing"
	projectPath       = "OpenScreenSharing.xcodeproj/project.pbxproj"
	buildPollInterval = 15 * time.Second
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: app_store_connect next_project_version | publish_testflight <project version> [release notes]")
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "next_project_version":
		err = printNextProjectVersion(ctx)
	case "publish_testflight":
		if len(os.Args) < 3 {
			log.Fatal("usage: app_store_connect publish_testflight <project version> [release notes]")
		}
		err = publishTestflight(ctx, os.Args[2], strings.Join(os.Args[3:], " "))
	default:
		log.Fatal("unknown action: ", os.Args[1])
	}
	if err != nil {
		log.Fatal(err)
	}
}

func createClient(expireDuration time.Duration) *asc.Client {
	privateKey, err := os.ReadFile(os.Getenv("ASC_KEY_PATH"))
	if err != nil {
		log.Fatal(err)
	}
	tokenConfig, err := asc.NewTokenConfig(os.Getenv("ASC_KEY_ID"), os.Getenv("ASC_KEY_ISSUER_ID"), expireDuration, privateKey)
	if err != nil {
		log.Fatal(err)
	}
	return asc.NewClient(tokenConfig.Client())
}

func readMarketingVersion() (string, error) {
	content, err := os.ReadFile(projectPath)
	if err != nil {
		return "", err
	}
	var project struct {
		Objects map[string]struct {
			ISA           string         `plist:"isa"`
			BuildSettings map[string]any `plist:"buildSettings"`
		} `plist:"objects"`
	}
	_, err = plist.Unmarshal(content, &project)
	if err != nil {
		return "", E.Cause(err, "parse project")
	}
	for _, object := range project.Objects {
		if object.ISA != "XCBuildConfiguration" || object.BuildSettings["PRODUCT_BUNDLE_IDENTIFIER"] != bundleID {
			continue
		}
		version, isString := object.BuildSettings["MARKETING_VERSION"].(string)
		if isString {
			return version, nil
		}
	}
	return "", E.New("missing MARKETING_VERSION for ", bundleID)
}

func printNextProjectVersion(ctx context.Context) error {
	marketingVersion, err := readMarketingVersion()
	if err != nil {
		return err
	}
	builds, _, err := createClient(time.Minute).Builds.ListBuilds(ctx, &asc.ListBuildsQuery{
		FilterApp:                       []string{appID},
		FilterPreReleaseVersionPlatform: []string{string(asc.PlatformIOS)},
		FilterPreReleaseVersionVersion:  []string{marketingVersion},
		Limit:                           200,
	})
	if err != nil {
		return err
	}
	nextProjectVersion := 1
	for _, build := range builds.Data {
		projectVersion, err := strconv.Atoi(*build.Attributes.Version)
		if err != nil {
			return E.Cause(err, "parse build number")
		}
		nextProjectVersion = max(nextProjectVersion, projectVersion+1)
	}
	os.Stdout.WriteString(F.ToString(nextProjectVersion, "\n"))
	return nil
}

func publishTestflight(ctx context.Context, projectVersion string, releaseNotes string) error {
	marketingVersion, err := readMarketingVersion()
	if err != nil {
		return err
	}
	if releaseNotes == "" {
		releaseNotes = F.ToString("Screen Sharing ", marketingVersion, " (", projectVersion, ")")
	}
	client := createClient(20 * time.Minute)
	query := &asc.ListBuildsQuery{
		FilterApp:                       []string{appID},
		FilterPreReleaseVersionPlatform: []string{string(asc.PlatformIOS)},
		FilterPreReleaseVersionVersion:  []string{marketingVersion},
		FilterVersion:                   []string{projectVersion},
		Limit:                           1,
	}
	name := F.ToString(marketingVersion, " (", projectVersion, ")")
	for {
		builds, _, err := client.Builds.ListBuilds(ctx, query)
		if err != nil {
			return err
		}
		if len(builds.Data) == 0 {
			log.Print(name, " waiting for build upload")
			time.Sleep(buildPollInterval)
			continue
		}
		build := builds.Data[0]
		if *build.Attributes.ProcessingState != "VALID" {
			log.Print(name, " waiting for process: ", *build.Attributes.ProcessingState)
			time.Sleep(buildPollInterval)
			continue
		}
		localizations, _, err := client.TestFlight.ListBetaBuildLocalizationsForBuild(ctx, build.ID, nil)
		if err != nil {
			return err
		}
		localization := common.Find(localizations.Data, func(it asc.BetaBuildLocalization) bool {
			return *it.Attributes.Locale == "en-US"
		})
		if localization.ID == "" {
			return E.New(name, " has no en-US localization")
		}
		if localization.Attributes.WhatsNew == nil || *localization.Attributes.WhatsNew == "" {
			log.Print(name, " update localization")
			_, _, err = client.TestFlight.UpdateBetaBuildLocalization(ctx, localization.ID, common.Ptr(releaseNotes))
			if err != nil {
				return err
			}
		}
		log.Print(name, " publish")
		response, err := client.TestFlight.AddBuildsToBetaGroup(ctx, groupID, []string{build.ID})
		if response != nil && (response.StatusCode == http.StatusUnprocessableEntity || response.StatusCode == http.StatusNotFound) {
			log.Print(name, " waiting for process")
			time.Sleep(buildPollInterval)
			continue
		} else if err != nil {
			return err
		}
		submissions, _, err := client.TestFlight.ListBetaAppReviewSubmissions(ctx, &asc.ListBetaAppReviewSubmissionsQuery{
			FilterBuild: []string{build.ID},
		})
		if err != nil {
			return err
		}
		if len(submissions.Data) == 0 {
			log.Print(name, " create submission")
			_, _, err = client.TestFlight.CreateBetaAppReviewSubmission(ctx, build.ID)
			if err != nil && strings.Contains(err.Error(), "ANOTHER_BUILD_IN_REVIEW") {
				log.Print(err)
				return nil
			}
			return err
		}
		return nil
	}
}
