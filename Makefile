BUILD_DIR ?= out

.PHONY: clean build install

build:
	mkdir -p $(BUILD_DIR)
	go build -installsuffix "static" -o $(BUILD_DIR)/docker-machine-driver-hyperkit

clean:
	rm -rf $(BUILD_DIR)

install: build
	chmod +x $(BUILD_DIR)/docker-machine-driver-hyperkit
	sudo mv $(BUILD_DIR)/docker-machine-driver-hyperkit /usr/local/bin/
	sudo chown root:wheel /usr/local/bin/docker-machine-driver-hyperkit
	sudo chmod u+s /usr/local/bin/docker-machine-driver-hyperkit
