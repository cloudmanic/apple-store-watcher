#
# Date: 2026-10-24
# Author: Spicer Matthews (spicer@cloudmanic.com)
# Copyright: 2026 Cloudmanic Labs, LLC. All rights reserved.
#

BINARY := apple-store-watcher

# "caffeine" on the command line (make run caffeine) wraps the run in
# caffeinate, so the Mac stays awake and the checks keep going.
ifneq ($(filter caffeine,$(MAKECMDGOALS)),)
RUN_PREFIX := caffeinate -i
endif

.DEFAULT_GOAL := build
.PHONY: build run caffeine test clean

# Build the watcher binary.
build:
	go build -o $(BINARY) .

# Build and start the watcher. Add "caffeine" to keep the Mac awake.
run: build
	$(RUN_PREFIX) ./$(BINARY)

# On its own, run with the Mac kept awake. With "run" it only sets the flag
# above, so the watcher does not start twice.
caffeine: build
ifeq ($(filter run,$(MAKECMDGOALS)),)
	caffeinate -i ./$(BINARY)
else
	@:
endif

# Run all tests.
test:
	go test -race -count=1 ./...

# Remove the built binary. The log, config and .env stay.
clean:
	rm -f $(BINARY)
