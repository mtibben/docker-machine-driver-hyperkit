# docker-machine-driver-hyperkit

A Hyperkit driver for docker-machine.

To install the hyperkit driver:

```shell
make install
```

The hyperkit driver currently requires running as root to use the vmnet framework to setup networking.

If you encountered errors like `Could not find hyperkit executable`, you might need to install [Docker for Mac](https://store.docker.com/editions/community/docker-ce-desktop-mac)
