# A typical hand-rolled Makefile, kept as an import demo for:
#   construct import examples/Makefile examples/Constfile-imported
#   construct examples/Constfile-imported --list
#   construct examples/Constfile-imported all
#
# It deliberately mixes clean constructs (vars, .PHONY, automatic vars,
# file targets) with make-only ones (ifeq, $(shell), a pattern rule) so the
# import summary shows both the conversion and the flagged lines.

CC := gcc
CFLAGS = -Wall -O2
TARGET = app
OBJS = main.o util.o

.PHONY: all clean check

all: $(TARGET)

# Link the binary from the object files
$(TARGET): $(OBJS)
	$(CC) $(CFLAGS) -o $@ $^

main.o: main.c
	$(CC) $(CFLAGS) -c $< -o $@

util.o: util.c
	$(CC) $(CFLAGS) -c $< -o $@

# Pattern rule: flagged by the importer (no construct equivalent)
%.o: %.c
	$(CC) -c $< -o $@

check: all
	./$(TARGET) --selftest

clean:
	@rm -f $(OBJS) $(TARGET)
	-touch .clean-marker || true

ifeq ($(shell uname),Darwin)
PLATFORM = macos
else
PLATFORM = other
endif

deploy: all
	@echo "deploying $(TARGET) to $(PLATFORM)"
	$(MAKE) -C scripts install
