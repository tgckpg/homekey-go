# Home Key Go on Home Assistant OS

## Local installation (no published image required)

After adding these files to the root of the homekey-go source repository:

```sh
sh scripts/stage-ha-app.sh
```

Copy the resulting `dist/homekey_go` directory into HA OS's `/addons` directory,
using the Samba `addons` share or an SSH app. The result must be
`/addons/homekey_go/config.yaml`, alongside its Dockerfile and source directories.
In HA, open Settings > Apps > App store, select Check for updates from the
three-dot menu, then install Home Key Go under Local apps.
On older HA versions the UI calls these Add-ons.

Enable Start on boot, start the app, and open Logs. Use the pairing code printed
by the Go service in Apple Home > Add Accessory > More Options > Go Home Key.
The container image build downloads Go dependencies, including the patched HAP
fork specified by go.mod. Internet access is needed during the initial build.

## Configuration

```yaml
name: Go Home Key
serial: GO-HOMEKEY-001
port: 51826
interface: ""
finish: silver
pin: ""
```

- `name`: accessory name shown in Apple Home.
- `serial`: stable accessory serial; keep it unchanged after pairing.
- `port`: HAP TCP port on the HA OS host. Change it if 51826 is occupied.
- `interface`: HA OS LAN interface, e.g. `end0` or `enp3s0`; empty selects
  eligible interfaces. Use the actual HA host interface name.
- `finish`: `silver`, `black`, `gold`, or `tan` Wallet artwork.
- `pin`: empty generates and saves a random PIN, then reuses it on later starts.
  An explicit PIN must have eight digits (hyphens are accepted by the CLI);
  prohibited HomeKit PIN patterns are rejected by the service.

Save configuration changes and restart the app.
The app uses host networking for HomeKit mDNS and binds HAP to `:<port>`.
Your phone must be able to reach the HA host and receive its mDNS advertisements.

## Persistent state and moving an existing pairing

The service is started with `-state /data/state`. HomeKit identity, paired
controllers, Home Key credentials, and the generated PIN are stored together
in `/data/state/state.json`. This is the app's persistent data volume, not the
HA configuration directory. App backups use cold mode so the service is stopped
while its state is backed up.

For a first installation you can pair a fresh accessory. To preserve an existing
pairing from your Mac, stop the Mac service and the HA app, then copy the entire
existing state directory into the app's `/data/state` through a method with
access to that app's data volume. The ordinary `/addons` source folder and HA
`/config` directory are NOT that volume. Keep the same name, serial, and explicit
PIN, if one was configured. Run only one instance with that identity.

Do not delete state or uninstall the app as an update procedure: losing the
state means losing its pairing identity and credentials. Keep an app backup.

## Building a container outside HA

From the source repository root:

```sh
docker build -t homekey-go:0.0.1 .
```

On a Linux Docker host, run it with a persistent volume and host networking:

```sh
docker run --rm --network host -v homekey-state:/data homekey-go:0.0.1
```

Without `/data/options.json`, the CLI uses its default accessory settings with
state fixed to `/data/state`. Explicit container arguments are passed directly
to the CLI, so include `-state /data/state` when supplying custom arguments:

```sh
docker run --rm --network host -v homekey-state:/data homekey-go:0.0.1 \
  -state /data/state -name 'Go Home Key' -addr :51827
```

Docker Desktop on macOS is not equivalent to a Linux HA OS host for LAN mDNS.
Use HA OS for the actual discovery and pairing test.

## Publishing and repository installation

`ha-app/config.yaml` is set up to pull `ghcr.io/tgckpg/homekey-go:0.0.1`.
That image has NOT been published by this packaging work. If you use another
registry/namespace, edit its `image` field. Build and push both supported
architectures before attempting repository installation:

```sh
docker login ghcr.io
docker buildx build --platform linux/amd64,linux/arm64 \
  --build-arg BUILD_ARCH='aarch64|amd64' \
  --build-arg BUILD_VERSION=0.0.1 \
  -t ghcr.io/tgckpg/homekey-go:0.0.1 --push .
```

Make the package public if HA will pull it without registry credentials.
Commit the packaging files to a cloneable Git repository, then add its Git URL
under App store > Repositories. Use the real Git repository endpoint, not the
static `sgit` HTML tree URL. Supervisor pulls the image using `config.yaml`.
The root `repository.yaml` declares this repository; `ha-app` contains the app.
Once the image is published, those metadata files can instead live in your
separate HA app repository; the root Dockerfile and Go source stay here.

For releases, update the app version in config.yaml and CHANGELOG.md, build and
publish the matching image tag, then commit the metadata. Preserve the app slug
and repository identity to keep existing installs on their update path.

## Current functionality

This packages the existing provisioning service unchanged. The HAP listener
supports Apple Home pairing and Home Key provisioning; lock toggles are still
simulated. No reader API, MQTT connection, Home Assistant entity integration,
or ESP32/PN532 authentication transport is added by these files.
