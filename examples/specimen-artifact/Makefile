XRAY := ../../xray
ARTIFACT := specimen.tar.gz
CONFIG := config.toml

.PHONY: regen inspect

regen:
	@if [ -z "$(GITHUB_TOKEN)" ]; then \
		echo "Error: GITHUB_TOKEN is not set. Export a GitHub PAT before running make regen."; \
		exit 1; \
	fi
	@sed "s/YOUR_GITHUB_TOKEN_HERE/$(GITHUB_TOKEN)/" $(CONFIG) > /tmp/xray-specimen.toml
	$(XRAY) run /tmp/xray-specimen.toml --out $(ARTIFACT)
	@rm -f /tmp/xray-specimen.toml

inspect:
	$(XRAY) inspect $(ARTIFACT)
