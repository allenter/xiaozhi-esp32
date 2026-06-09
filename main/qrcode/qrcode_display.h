#pragma once

#include <string>

// Generate and display a QR code from the given URL string.
// Uses LVGL to draw the QR code on the current display.
// The QR code is displayed over the current screen with black modules on white background.
// Returns true on success, false on failure.
bool ShowQrCode(const std::string& url, int timeout_seconds = 60);

// Hide the QR code and restore the normal display.
void HideQrCode();
