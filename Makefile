.PHONY: nc upgrade

build-upgrade:
	go build -o upgrade/upgrade upgrade/main.go

build-transfer:
	go build -o service/transfer service/main.go

nc:
	nc 127.0.0.1 7000

tail-transfer:
	journalctl -u transfer.service -f

tail-upgrade:
	journalctl -u upgrade.service -f

upgrade:
	sudo systemctl kill --signal=USR1 transfer.service

