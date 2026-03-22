.PHONY: build run clean test deps deploy-compiled compile-only

# Build the application
build:
	go build -o socket-proxy-service cmd/main.go

# Run the application
run: build
	./socket-proxy-service -config config.yml

# Clean build artifacts
clean:
	rm -f socket-proxy-service
	rm -f socket-proxy.log

# Run tests
test:
	go test ./...

# Download dependencies
deps:
	go mod tidy
	go mod download

# Format code
fmt:
	go fmt ./...

# Run linter
lint:
	golangci-lint run

# Build for Linux
build-linux:
	GOOS=linux GOARCH=amd64 go build -o socket-proxy-service-linux cmd/main.go

# Build for Raspberry Pi
build-rpi:
	GOOS=linux GOARCH=arm64 go build -o socket-proxy-service-rpi cmd/main.go

# Install dependencies
install: build
	sudo cp socket-proxy-service /usr/local/bin/

# Create systemd service
create-service:
	sudo cp socket-proxy-service.service /etc/systemd/system/
	sudo systemctl daemon-reload
	sudo systemctl enable socket-proxy-service

# Start systemd service
start-service:
	sudo systemctl start socket-proxy-service

# Stop systemd service
stop-service:
	sudo systemctl stop socket-proxy-service

# Check service status
status-service:
	sudo systemctl status socket-proxy-service

# View logs
logs:
	sudo journalctl -u socket-proxy-service -f

# Compile and deploy to compiled-edge-agent repository (Raspberry Pi build)
deploy-compiled: clean
	@echo "Building edge-agent for Raspberry Pi (ARM64)..."
	GOOS=linux GOARCH=arm64 go build -o edge-agent cmd/main.go
	@echo "Creating compiled directory..."
	mkdir -p compiled
	@echo "Copying files to compiled directory..."
	cp edge-agent compiled/
	cp config.yml compiled/
	@echo "Initializing git repository in compiled directory..."
	cd compiled && \
	if [ ! -d .git ]; then \
		git init; \
		git remote add origin git@hub.mos.ru:atm-post/compiled-edge-agent.git; \
	fi
	@echo "Adding and committing files..."
	cd compiled && \
	git add edge-agent config.yml; \
	git commit -m "Deploy edge-agent for Raspberry Pi with config - $$(date +'%Y-%m-%d %H:%M:%S')" || echo "No changes to commit"
	@echo "Pushing to repository..."
	cd compiled && \
	git push origin main || git push origin master || echo "Push failed - check branch name"
	@echo "Deployment completed successfully!"

# Quick compile only for Raspberry Pi (without git operations)
compile-only: clean
	@echo "Building edge-agent for Raspberry Pi (ARM64)..."
	GOOS=linux GOARCH=arm64 go build -o edge-agent cmd/main.go
	@echo "Creating compiled directory..."
	mkdir -p compiled
	@echo "Copying files to compiled directory..."
	cp edge-agent compiled/
	cp config.yml compiled/
	@echo "Compilation completed!"
