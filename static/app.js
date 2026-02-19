// API base URL
const API_BASE = '/api';

// Toast helper
function showToast(message, isError = false) {
    const toast = document.getElementById('toast');
    const toastMessage = document.getElementById('toastMessage');
    toastMessage.textContent = message;
    toast.className = `toast align-items-center ${isError ? 'bg-danger text-white' : 'bg-success text-white'}`;
    const bsToast = new bootstrap.Toast(toast);
    bsToast.show();
}

// Format date
function formatDate(dateStr) {
    if (!dateStr || dateStr === '0001-01-01T00:00:00Z') return 'Never';
    const date = new Date(dateStr);
    return date.toLocaleString();
}

// Load status
async function loadStatus() {
    try {
        const response = await fetch(`${API_BASE}/status`);
        const data = await response.json();
        if (data.success) {
            document.getElementById('status').innerHTML = `
                <span class="badge bg-success">Running</span>
                <span class="ms-3"><strong>Channels:</strong> ${data.data.channels_count}</span>
                <span class="ms-3"><strong>Videos:</strong> ${data.data.videos_count}</span>
            `;
        }
    } catch (error) {
        document.getElementById('status').innerHTML = '<span class="badge bg-danger">Error</span>';
    }
}

// Load channels
async function loadChannels() {
    try {
        const response = await fetch(`${API_BASE}/channels`);
        const data = await response.json();
        if (data.success) {
            const channels = data.data || [];
            if (channels.length === 0) {
                document.getElementById('channelsList').innerHTML = '<p class="text-muted">No channels configured</p>';
            } else {
                document.getElementById('channelsList').innerHTML = channels.map((ch, idx) => {
                    const cutoffText = ch.cutoff_date && ch.cutoff_date !== '0001-01-01T00:00:00Z' 
                        ? `<span class="badge bg-info ms-2">From: ${new Date(ch.cutoff_date).toLocaleDateString()}</span>` 
                        : '';
                    const hasError = ch.last_error && ch.last_error.trim().length > 0;
                    const errorBadge = hasError 
                        ? `<span class="badge bg-danger ms-2"><i class="bi bi-exclamation-circle"></i> Error</span>` 
                        : '';
                    const errorSection = hasError 
                        ? `
                            <div class="mt-2">
                                <button class="btn btn-sm btn-outline-danger collapsed" type="button" data-bs-toggle="collapse" data-bs-target="#error-${idx}" aria-expanded="false">
                                    <i class="bi bi-chevron-down"></i> View Error Details
                                </button>
                                <div class="collapse mt-2" id="error-${idx}">
                                    <div class="alert alert-danger" role="alert">
                                        <strong>Error:</strong> ${ch.last_error}<br>
                                        <small class="text-muted">At: ${formatDate(ch.last_error_time)}</small>
                                    </div>
                                </div>
                            </div>
                        ` 
                        : '';
                    return `
                        <div class="d-flex justify-content-between align-items-start border-bottom py-3">
                            <div style="flex-grow: 1;">
                                <strong>${ch.name}</strong>
                                <span class="badge bg-secondary ms-2">${ch.retention_days || 'default'} days</span>
                                ${cutoffText}
                                ${errorBadge}<br>
                                <small class="text-muted">${ch.url}</small><br>
                                <span class="last-checked">Last checked: ${formatDate(ch.last_checked)}</span>
                                ${errorSection}
                            </div>
                            <button class="btn btn-danger btn-sm" onclick="removeChannel('${ch.id}')">
                                <i class="bi bi-trash"></i> Remove
                            </button>
                        </div>
                    `;
                }).join('');
            }
        }
    } catch (error) {
        document.getElementById('channelsList').innerHTML = '<p class="text-danger">Failed to load channels</p>';
    }
}

// Load videos
async function loadVideos() {
    try {
        const response = await fetch(`${API_BASE}/videos`);
        const data = await response.json();
        if (data.success) {
            const videos = data.data || [];
            if (videos.length === 0) {
                document.getElementById('videosList').innerHTML = '<p class="text-muted">No videos configured</p>';
            } else {
                document.getElementById('videosList').innerHTML = videos.map((vid, idx) => {
                    const hasError = vid.last_error && vid.last_error.trim().length > 0;
                    const errorBadge = hasError 
                        ? `<span class="badge bg-danger ms-2"><i class="bi bi-exclamation-circle"></i> Error</span>` 
                        : '';
                    const errorSection = hasError 
                        ? `
                            <div class="mt-2">
                                <button class="btn btn-sm btn-outline-danger collapsed" type="button" data-bs-toggle="collapse" data-bs-target="#video-error-${idx}" aria-expanded="false">
                                    <i class="bi bi-chevron-down"></i> View Error Details
                                </button>
                                <div class="collapse mt-2" id="video-error-${idx}">
                                    <div class="alert alert-danger" role="alert">
                                        <strong>Error:</strong> ${vid.last_error}<br>
                                        <small class="text-muted">At: ${formatDate(vid.last_error_time)}</small>
                                    </div>
                                </div>
                            </div>
                        ` 
                        : '';
                    return `
                        <div class="d-flex justify-content-between align-items-start border-bottom py-3">
                            <div style="flex-grow: 1;">
                                <strong>${vid.title}</strong>
                                <span class="badge bg-secondary ms-2">${vid.retention_days || 'default'} days</span>
                                ${errorBadge}<br>
                                <small class="text-muted">${vid.url}</small><br>
                                <span class="last-checked">Last checked: ${formatDate(vid.last_checked)}</span>
                                ${errorSection}
                            </div>
                            <button class="btn btn-danger btn-sm" onclick="removeVideo('${vid.id}')">
                                <i class="bi bi-trash"></i> Remove
                            </button>
                        </div>
                    `;
                }).join('');
            }
        }
    } catch (error) {
        document.getElementById('videosList').innerHTML = '<p class="text-danger">Failed to load videos</p>';
    }
}

// Load config
async function loadConfig() {
    try {
        const response = await fetch(`${API_BASE}/config`);
        const data = await response.json();
        if (data.success) {
            const config = data.data;
            document.getElementById('checkInterval').value = config.check_interval_seconds;
            document.getElementById('retentionDays').value = config.retention_days;
            document.getElementById('downloadDir').value = config.download_dir;
            document.getElementById('fileNamePattern').value = config.file_name_pattern;
            document.getElementById('maxConcurrent').value = config.max_concurrent_downloads;
            document.getElementById('ytDlpUpdateInterval').value = config.yt_dlp_update_interval_seconds;
            document.getElementById('cookiesBrowser').value = config.cookies_browser || '';
            // cookiesFile is set by backend, not shown in UI
        }
    } catch (error) {
        showToast('Failed to load configuration', true);
    }
}

// Save pasted cookies with confirmation
document.getElementById('saveCookiesBtn').addEventListener('click', async () => {
    const cookieText = document.getElementById('cookiesPaste').value.trim();
    if (!cookieText) {
        showToast('Please paste some cookies first', true);
        return;
    }

    // Check if cookies already exist
    let confirmMessage = 'Save these cookies?';
    try {
        const response = await fetch(`${API_BASE}/config`);
        const data = await response.json();
        if (data.data && data.data.cookies_file) {
            confirmMessage = 'This will overwrite your existing cookies. Continue?';
        }
    } catch (e) {
        // Continue regardless
    }

    if (!confirm(confirmMessage)) {
        return;
    }

    try {
        const response = await fetch(`${API_BASE}/cookies`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ cookie_text: cookieText })
        });
        const data = await response.json();
        if (data.success) {
            showToast('Cookies saved successfully!');
            document.getElementById('cookiesPaste').value = '';
            loadConfig();
        } else {
            showToast(data.message || 'Failed to save cookies', true);
        }
    } catch (error) {
        showToast('Failed to save cookies', true);
    }
});

// Clear all cookies
document.getElementById('clearCookiesBtn').addEventListener('click', async () => {
    if (!confirm('Are you sure you want to clear all cookies? This cannot be undone.')) {
        return;
    }

    try {
        const response = await fetch(`${API_BASE}/cookies/clear`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' }
        });
        const data = await response.json();
        if (data.success) {
            showToast('All cookies cleared!');
            document.getElementById('cookiesPaste').value = '';
            loadConfig();
        } else {
            showToast(data.message || 'Failed to clear cookies', true);
        }
    } catch (error) {
        showToast('Failed to clear cookies', true);
    }
});

// Add channel
document.getElementById('addChannelForm').addEventListener('submit', async (e) => {
    e.preventDefault();
    const name = document.getElementById('channelName').value;
    const url = document.getElementById('channelURL').value;
    const retention = parseInt(document.getElementById('channelRetention').value) || 0;
    const cutoffDate = document.getElementById('channelCutoffDate').value;

    const channelData = { name, url, retention_days: retention };
    if (cutoffDate) {
        channelData.cutoff_date = new Date(cutoffDate).toISOString();
    }

    try {
        const response = await fetch(`${API_BASE}/channels`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(channelData)
        });
        const data = await response.json();
        if (data.success) {
            showToast('Channel added successfully');
            document.getElementById('addChannelForm').reset();
            loadChannels();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to add channel', true);
        }
    } catch (error) {
        showToast('Failed to add channel', true);
    }
});

// Remove channel
async function removeChannel(id) {
    if (!confirm('Are you sure you want to remove this channel?')) return;

    try {
        const response = await fetch(`${API_BASE}/channels/${id}`, { method: 'DELETE' });
        const data = await response.json();
        if (data.success) {
            showToast('Channel removed successfully');
            loadChannels();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to remove channel', true);
        }
    } catch (error) {
        showToast('Failed to remove channel', true);
    }
}

// Add video
document.getElementById('addVideoForm').addEventListener('submit', async (e) => {
    e.preventDefault();
    const title = document.getElementById('videoTitle').value;
    const url = document.getElementById('videoURL').value;
    const retention = parseInt(document.getElementById('videoRetention').value) || 0;

    try {
        const response = await fetch(`${API_BASE}/videos`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ title, url, retention_days: retention })
        });
        const data = await response.json();
        if (data.success) {
            showToast('Video added successfully');
            document.getElementById('addVideoForm').reset();
            loadVideos();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to add video', true);
        }
    } catch (error) {
        showToast('Failed to add video', true);
    }
});

// Remove video
async function removeVideo(id) {
    if (!confirm('Are you sure you want to remove this video?')) return;

    try {
        const response = await fetch(`${API_BASE}/videos/${id}`, { method: 'DELETE' });
        const data = await response.json();
        if (data.success) {
            showToast('Video removed successfully');
            loadVideos();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to remove video', true);
        }
    } catch (error) {
        showToast('Failed to remove video', true);
    }
}

// Update config
document.getElementById('configForm').addEventListener('submit', async (e) => {
    e.preventDefault();
    const config = {
        check_interval_seconds: parseInt(document.getElementById('checkInterval').value),
        retention_days: parseInt(document.getElementById('retentionDays').value),
        download_dir: document.getElementById('downloadDir').value,
        file_name_pattern: document.getElementById('fileNamePattern').value,
        max_concurrent_downloads: parseInt(document.getElementById('maxConcurrent').value),
        yt_dlp_update_interval_seconds: parseInt(document.getElementById('ytDlpUpdateInterval').value),
        cookies_browser: document.getElementById('cookiesBrowser').value,
        cookies_file: ""  // Set by cookie paste endpoint
    };

    try {
        const response = await fetch(`${API_BASE}/config`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(config)
        });
        const data = await response.json();
        if (data.success) {
            showToast('Configuration saved successfully');
        } else {
            showToast(data.message || 'Failed to save configuration', true);
        }
    } catch (error) {
        showToast('Failed to save configuration', true);
    }
});

// Initial load
loadStatus();
loadChannels();
loadVideos();
loadConfig();

// Refresh data every 30 seconds
setInterval(() => {
    loadStatus();
    loadChannels();
    loadVideos();
}, 30000);
