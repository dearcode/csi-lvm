cmd := $(shell ls cmd/)

all: $(cmd)

clean:
	@rm -rf bin


$(cmd):
	go build -o bin/$@ -ldflags '$(LDFLAGS)' cmd/$@/*.go

