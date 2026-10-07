SHELL := /bin/bash
.SHELLFLAGS := -o pipefail -c
.SILENT:

lib:
	go run ./cmd/internal/build_libsharing

build_ios:
	xcodebuild build -scheme OpenScreenSharing -configuration Debug -destination 'generic/platform=iOS' | xcbeautify | grep -A 10 -e "Build Succeeded" -e "BUILD FAILED" -e "❌"

release_ios: lib archive_ios upload_ios

archive_ios:
	rm -rf build/OpenScreenSharing-iOS.xcarchive
	xcodebuild archive $(XCODEBUILD_FLAGS) -scheme OpenScreenSharing -configuration Release -destination 'generic/platform=iOS' -archivePath build/OpenScreenSharing-iOS.xcarchive | xcbeautify

upload_ios:
	xcodebuild -exportArchive -archivePath build/OpenScreenSharing-iOS.xcarchive -exportOptionsPlist OpenScreenSharing/Upload.plist

export_ios:
	rm -rf build/OpenScreenSharing-iOS
	xcodebuild -exportArchive -archivePath build/OpenScreenSharing-iOS.xcarchive -exportOptionsPlist OpenScreenSharing/Export.plist -exportPath build/OpenScreenSharing-iOS

build_ios_ipa: lib archive_ios_ipa

archive_ios_ipa:
	rm -rf build/OpenScreenSharing-Sideload.xcarchive build/Payload build/OpenScreenSharing.ipa
	xcodebuild archive $(XCODEBUILD_FLAGS) -scheme OpenScreenSharing -configuration Release -destination 'generic/platform=iOS' -archivePath build/OpenScreenSharing-Sideload.xcarchive \
		CODE_SIGNING_ALLOWED=NO SWIFT_ACTIVE_COMPILATION_CONDITIONS=SIDELOAD INFOPLIST_PREPROCESSOR_DEFINITIONS=SIDELOAD=1 EXCLUDED_SOURCE_FILE_NAMES= | xcbeautify
	mkdir build/Payload
	cp -R build/OpenScreenSharing-Sideload.xcarchive/Products/Applications/Screen\ Sharing.app build/Payload/
	cd build && zip -qry OpenScreenSharing.ipa Payload
	rm -rf build/Payload

altstore_source:
	go run ./cmd/internal/build_altstore_source

screenshots_ios:
	go run ./cmd/internal/screenshots

fmt:
	gofumpt -l -w cmd libsharing internal
