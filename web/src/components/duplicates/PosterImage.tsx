import { clsx } from 'clsx';
import { Film, Tv } from 'lucide-react';
import { useState } from 'react';
import { mediaCoverUrl } from '@/api/hooks/useMediaServers';
import type { Id, MediaType } from '@/api/types';

export interface PosterImageProps {
  serverId: Id;
  /** Plex thumb path; placeholder when empty. */
  thumb?: string | null;
  mediaType: MediaType;
  /** Requested image size from the poster proxy (px). */
  width?: number;
  height?: number;
  className?: string;
  /** Accessible text (empty = decorative). */
  alt?: string;
}

/**
 * Lazy-loaded poster from the media-cover proxy with a film/tv placeholder when the item has no
 * thumb or the image fails to load. Always 2:3.
 */
export function PosterImage({
  serverId,
  thumb,
  mediaType,
  width = 150,
  height = 225,
  className,
  alt = '',
}: PosterImageProps) {
  const src = thumb && serverId > 0 ? mediaCoverUrl(serverId, thumb, width, height) : null;
  const [failedSrc, setFailedSrc] = useState<string | null>(null);
  const Icon = mediaType === 'episode' ? Tv : Film;
  const showImage = src !== null && failedSrc !== src;

  return (
    <div
      className={clsx(
        'relative aspect-[2/3] shrink-0 overflow-hidden rounded-sm bg-card-hover text-subtle',
        className,
      )}
    >
      {showImage ? (
        <img
          src={src}
          alt={alt}
          loading="lazy"
          decoding="async"
          draggable={false}
          onError={() => setFailedSrc(src)}
          className="absolute inset-0 size-full object-cover"
        />
      ) : (
        <div className="absolute inset-0 flex items-center justify-center" aria-hidden={alt ? undefined : true}>
          <Icon width="40%" height="40%" aria-hidden />
          {alt && <span className="sr-only">{alt}</span>}
        </div>
      )}
    </div>
  );
}
