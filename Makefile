BINARY = peon
HELPER = peon-helper.exe
INSTALL_DIR = $(HOME)/.claude/hooks/peon-ping

.PHONY: build install clean

build:
	go build -o $(BINARY) .
	GOOS=windows GOARCH=amd64 go build -ldflags "-H windowsgui" -o $(HELPER) ./helper/

install: build
	cp $(BINARY) $(INSTALL_DIR)/$(BINARY)
	cp $(HELPER) $(INSTALL_DIR)/$(HELPER)

clean:
	rm -f $(BINARY) $(HELPER)
