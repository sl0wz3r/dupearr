import { Eye, EyeOff } from 'lucide-react';
import { useState } from 'react';
import { IconButton } from './IconButton';
import { TextInput, type TextInputProps } from './TextInput';

export type PasswordInputProps = Omit<TextInputProps, 'type' | 'suffix'>;

/**
 * Password / secret input with a show/hide toggle. API returns secrets masked as "********";
 * leave the mask untouched to keep the stored value.
 */
export function PasswordInput({ autoComplete = 'off', ...rest }: PasswordInputProps) {
  const [visible, setVisible] = useState(false);
  return (
    <TextInput
      {...rest}
      type={visible ? 'text' : 'password'}
      autoComplete={autoComplete}
      spellCheck={false}
      suffix={
        <IconButton
          icon={visible ? EyeOff : Eye}
          label={visible ? 'Hide' : 'Show'}
          size="sm"
          onClick={() => setVisible((v) => !v)}
          tabIndex={-1}
        />
      }
    />
  );
}
