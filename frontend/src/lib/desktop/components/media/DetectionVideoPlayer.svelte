<script lang="ts">
  import { t } from '$lib/i18n';
  import type { Detection } from '$lib/types/detection.types';
  import { buildAppUrl } from '$lib/utils/urlHelpers';

  interface Props {
    detection: Detection;
    className?: string;
  }

  let { detection, className = '' }: Props = $props();

  let videoUrl = $derived(buildAppUrl(`/api/v2/video/${detection.id}`));
  let posterUrl = $derived.by(() => {
    if (detection.videoPreviewImageUrl) {
      return buildAppUrl(detection.videoPreviewImageUrl);
    }

    return buildAppUrl(`/api/v2/video/${detection.id}/poster`);
  });
</script>

<div class={className}>
  <video
    class="detection-video-player"
    controls
    preload="metadata"
    playsinline
    poster={posterUrl}
    aria-label={t('detections.media.videoAria', { species: detection.commonName })}
  >
    <source src={videoUrl} type="video/mp4" />
    {t('detections.media.videoUnsupported')}
  </video>
</div>

<style>
  .detection-video-player {
    width: 100%;
    border-radius: 0.75rem;
    background: linear-gradient(
      135deg,
      color-mix(in srgb, var(--color-base-200) 85%, transparent) 0%,
      color-mix(in srgb, var(--color-base-300) 70%, transparent) 100%
    );
    aspect-ratio: 16 / 9;
  }
</style>
