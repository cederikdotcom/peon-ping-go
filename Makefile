BINARY = peon
INSTALL_DIR = $(HOME)/.claude/hooks/peon-ping

.PHONY: build install clean

build:
	go build -o $(BINARY) .

install: build
	cp $(BINARY) $(INSTALL_DIR)/$(BINARY)

clean:
	rm -f $(BINARY)
