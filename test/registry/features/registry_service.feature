Feature: Registry write service
  As the operator of a public fpm registry
  I want publishing to be authenticated, verified and atomic
  So that the catalogue can be trusted by people who did not publish to it

  # The service replaces nginx's WebDAV on the write path only. Reads stay
  # static files, so an unmodified fpm client must not be able to tell the
  # difference beyond the base URL.

  Background:
    Given a running registry service
    And a publisher token for org "acme"

  # ── The CLI contract is preserved exactly ─────────────────────────

  Scenario: An artifact upload returns a status the CLI accepts
    When the publisher uploads "acme/widget/1.0.0/widget-1.0.0.fpm"
    Then the response status should be one of 200, 201, 202, 204

  Scenario: The published artifact can be downloaded back
    Given the publisher has uploaded "acme/widget/1.0.0/widget-1.0.0.fpm"
    When an anonymous client downloads that artifact
    Then the response status should be 200
    And the bytes should match what was uploaded

  Scenario: Package metadata is generated from the artifact
    Given the publisher has uploaded "acme/widget/1.0.0/widget-1.0.0.fpm"
    When an anonymous client reads "/metadata/acme/widget/package-metadata.json"
    Then the response status should be 200
    And the metadata should list version "1.0.0"
    And the metadata should carry the declared Frappe compatibility

  Scenario: The catalogue index is generated
    Given the publisher has uploaded "acme/widget/1.0.0/widget-1.0.0.fpm"
    When an anonymous client reads "/metadata/index.json"
    Then the index should contain "acme/widget"

  Scenario: Metadata written by the client is accepted but not trusted
    # fpm publish PUTs metadata after the artifact. Rejecting it would break
    # the client; believing it would reopen the forgery hole, since the client
    # supplies fpm_path and checksum_sha256.
    When the publisher writes package metadata claiming a forged checksum
    Then the response status should be one of 200, 201, 202, 204
    And the stored metadata should still carry the real checksum

  # ── Authentication and ownership ──────────────────────────────────

  Scenario: Anonymous publishing is refused
    When an anonymous client uploads "acme/widget/1.0.0/widget-1.0.0.fpm"
    Then the response status should be 401

  Scenario: A publisher cannot write to another organisation
    When the publisher uploads "otherorg/widget/1.0.0/widget-1.0.0.fpm"
    Then the response status should be 403

  Scenario: An unknown token is refused
    When a client with an invalid token uploads an artifact
    Then the response status should be 401

  Scenario: Reads never require credentials
    When an anonymous client reads "/metadata/index.json"
    Then the response status should be 200

  # ── Integrity ─────────────────────────────────────────────────────

  Scenario: An artifact whose bytes disagree with its manifest is refused
    When the publisher uploads an artifact whose content checksum does not match
    Then the response status should be 400
    And nothing should be stored for that version

  Scenario: An artifact that is not a valid package is refused
    When the publisher uploads bytes that are not an fpm archive
    Then the response status should be 400

  Scenario: The coordinates in the path must match the manifest
    # Otherwise a publisher could upload someone else's package under their own
    # path, or claim a version the artifact does not declare.
    When the publisher uploads a widget artifact to the path for "gadget"
    Then the response status should be 400

  # ── Correctness the static registry could not provide ─────────────

  Scenario: The latest version is computed by semantic precedence
    Given the publisher has uploaded version "1.9.0"
    And the publisher has uploaded version "1.10.0"
    When an anonymous client reads the package metadata
    Then the latest version should be "1.10.0"

  Scenario: Republishing an existing version is refused
    Given the publisher has uploaded "acme/widget/1.0.0/widget-1.0.0.fpm"
    When the publisher uploads that same version again
    Then the response status should be 409

  Scenario: Concurrent publishes both survive
    # The static registry lost one of these: publish was a read-modify-write of
    # a single shared index.json with no locking.
    When two different packages are published at the same moment
    Then the index should contain both

  Scenario: Downloads are counted
    Given the publisher has uploaded "acme/widget/1.0.0/widget-1.0.0.fpm"
    When the artifact is downloaded twice
    Then the recorded download count should be 2
