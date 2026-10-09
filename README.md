# Open Screen Sharing

Open Screen Sharing aims to develop superior servers and clients based on the private protocol used by Apple VNC (macOS's Screen Sharing server and ScreenSharing.app).

## Features

1. Compared to the original ScreenSharing.app:

* Optimized for local networks
* Optimized for public networks (runs smoothly with Tailscale P2P)
* Fixed language switching
* Exclusive mouse support
* Debug HUD support
* Interactions optimized for iOS and iPadOS

2. Apple VNC server implementation on iOS and iPadOS

* Requires iOS 27 (Xcode DeviceHub); no jailbreak or TrollStore required (though sideloading is obviously necessary)
* Based on the iOS developer debugging protocol (requires [LocalDevVPN](https://apps.apple.com/app/id6755608044), or [sing-box](https://sing-box.sagernet.org) with `loopback_address`; For reference, an example of a usable configuration: [LocalDevVPN.json](examples/sing-box-loopback-example/LocalDevVPN.json))
* It can be connected to using the native macOS ScreenSharing.app (note: due to an Apple bug, you must go to Settings > Quality and disable "Full Quality" before adding the configuration)
* Our client is optimized for it

## Roadmap

* [x] Add support for macOS
* [ ] Add support for RDP
* [ ] Add support for other operating systems, such as Android, Windows, and Linux
* [ ] Add a jailbreak version to provide numerous privilege-based improvements
* [ ] Create a protocol extension to support high refresh rates on Mac, touch passthrough on Mac (similar to Sidecar), Smaller scaling at native quality on Mac, multi-touch support on iOS, and further protocol optimizations

Given the complexity of custom protocols, we are currently focusing on improving the existing experience and expanding support based entirely on the Apple VNC protocol

## License

```
Copyright (C) 2026 by nekohasekai <contact-sagernet@sekai.icu>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.

In addition, no derivative work may use the name or imply association
with this application without prior consent.
```