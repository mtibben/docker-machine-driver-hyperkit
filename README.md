# zazula/docker-machine-driver-hyperkit

Mods to default hyperkit machine driver to allow it to run on MacOS

Primarily: https://github.com/machine-drivers/docker-machine-driver-hyperkit/pull/14

```brew install golang dep
go get github.com/zazula/docker-machine-driver-hyperkit
cd ~/go/src/github.com/zazula/docker-machine-driver-hyperkit
make build

docker-machine -D create --driver hyperkit --engine-env DOCKER_RAMDISK=true --hyperkit-cpu-count 12 --hyperkit-disk-size 40960 --hyperkit-memory-size 16192  local
[...]

$eval $(docker-machine env local)
```

Make sure that `hyperkit` binary is setuid `root` as well.

# docker-machine-driver-hyperkit (ORIGINAL README)

The Hyperkit driver will eventually replace the existing xhyve driver and uses [moby/hyperkit](http://github.com/moby/hyperkit) as a Go library.

To install the hyperkit driver:

```shell
make build
```

The hyperkit driver currently requires running as root to use the vmnet framework to setup networking.

If you encountered errors like `Could not find hyperkit executable`, you might need to install [Docker for Mac](https://store.docker.com/editions/community/docker-ce-desktop-mac)
