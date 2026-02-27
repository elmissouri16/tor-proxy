# Use the official Golang image as the base image
FROM golang:1.24-alpine

# Install system dependencies required for Tor
RUN apk add --no-cache \
    tor \
    curl

# Set the working directory inside the container
WORKDIR /app

# Copy tor configuration
COPY torrc /etc/tor/torrc

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download all dependencies
RUN go mod download

# Copy the source code into the container
COPY . .

# Build the Go app
RUN go build -o tor-proxy .

# Expose HTTP API and SOCKS5 ports
EXPOSE 8080 9050

# Command to run the executable
CMD ["./tor-proxy"]
