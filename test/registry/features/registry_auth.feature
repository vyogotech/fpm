Feature: Registry write protection
  As the operator of a public fpm registry
  I want every write path to require credentials
  So that a third party cannot forge package metadata or artifacts

  Background:
    Given a registry serving the repository nginx configuration
    And a publisher credential "publisher" exists

  # ── Public reads stay public ───────────────────────────────────────

  Scenario: Anyone may read the catalogue index
    When an anonymous client sends GET to "/metadata/index.json"
    Then the response status should be 200

  Scenario: Anyone may read package metadata
    When an anonymous client sends GET to "/metadata/acme/widget/package-metadata.json"
    Then the response status should be 200

  Scenario: Anyone may download an artifact
    When an anonymous client sends GET to "/acme/widget/1.0.0/widget-1.0.0.fpm"
    Then the response status should be 200

  Scenario: The health endpoint stays open for probes
    When an anonymous client sends GET to "/health"
    Then the response status should be 200

  # ── Metadata writes are the checksum-forgery surface ───────────────
  # package-metadata.json carries fpm_path and checksum_sha256, so an
  # anonymous writer could repoint a package at any artifact and forge
  # its checksum. index.json governs the whole catalogue.

  Scenario: An anonymous client cannot overwrite the catalogue index
    When an anonymous client sends PUT to "/metadata/index.json"
    Then the response status should be 401

  Scenario: An anonymous client cannot overwrite package metadata
    When an anonymous client sends PUT to "/metadata/acme/widget/package-metadata.json"
    Then the response status should be 401

  Scenario: An anonymous client cannot delete package metadata
    When an anonymous client sends DELETE to "/metadata/acme/widget/package-metadata.json"
    Then the response status should be 401

  # ── Artifact writes at every path depth ───────────────────────────
  # The server-level regex only matches paths of depth 3 or more, so a
  # shallow path falls through to the nested location instead.

  Scenario: An anonymous client cannot write a well-formed artifact path
    When an anonymous client sends PUT to "/acme/widget/1.0.0/widget-1.0.0.fpm"
    Then the response status should be 401

  Scenario: An anonymous client cannot write a shallow artifact path
    When an anonymous client sends PUT to "/acme/evil.fpm"
    Then the response status should be 401

  Scenario: An anonymous client cannot write a top-level artifact path
    When an anonymous client sends PUT to "/evil.fpm"
    Then the response status should be 401

  # ── Credentialed publishing must keep working ─────────────────────

  Scenario: A credentialed publisher may upload an artifact
    When the publisher sends PUT to "/acme/widget/2.0.0/widget-2.0.0.fpm"
    Then the response status should be one of 200, 201, 204

  Scenario: A credentialed publisher may update package metadata
    When the publisher sends PUT to "/metadata/acme/widget/package-metadata.json"
    Then the response status should be one of 200, 201, 204

  Scenario: A credentialed publisher may update the catalogue index
    When the publisher sends PUT to "/metadata/index.json"
    Then the response status should be one of 200, 201, 204

  # ── CORS must survive the nested locations ────────────────────────
  # A nested location that declares its own add_header discards every
  # add_header inherited from its parent, so the CORS headers vanish on
  # exactly the paths a browser client needs.

  Scenario: Catalogue index reads carry CORS headers
    When an anonymous client sends GET to "/metadata/index.json"
    Then the response should have header "Access-Control-Allow-Origin" set to "*"

  Scenario: Package metadata reads carry CORS headers
    When an anonymous client sends GET to "/metadata/acme/widget/package-metadata.json"
    Then the response should have header "Access-Control-Allow-Origin" set to "*"

  Scenario: Artifact downloads carry CORS headers
    When an anonymous client sends GET to "/acme/widget/1.0.0/widget-1.0.0.fpm"
    Then the response should have header "Access-Control-Allow-Origin" set to "*"
