.PHONY: build clean install

docker-machine-driver-hyperkit:
	go build -installsuffix "static" -o docker-machine-driver-hyperkit

build: docker-machine-driver-hyperkit

clean:
	rm -f docker-machine-driver-hyperkit

install: build
	chmod +x docker-machine-driver-hyperkit
	sudo mv docker-machine-driver-hyperkit /usr/local/bin/
	sudo chown root:wheel /usr/local/bin/docker-machine-driver-hyperkit
	sudo chmod u+s /usr/local/bin/docker-machine-driver-hyperkit
