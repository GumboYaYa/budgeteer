from django import forms
from .models import Account, Currency, Optionee, Transaction


class FileUploadForm(forms.Form):

    file = forms.FileField(required=False)
    iban = forms.CharField(max_length=24, strip=True)
    currency = forms.CharField(max_length=3, strip=True)
    optionee_name = forms.CharField(max_length=200, strip=True)
    optionee_iban = forms.CharField(max_length=22, strip=True)
    optionee_bic = forms.CharField(max_length=11, strip=True)
    date_booking = forms.DateField()
    figure = forms.DecimalField(max_digits=18, decimal_places=2)
    reference = forms.CharField(max_length=500, strip=True)

    def save(self):
        data = self.cleaned_data
        # data = self

        acc, created = Account.objects.get_or_create(iban=data["iban"])
        if created:
            acc.save()

        curr, created = Currency.objects.get_or_create(currency_short=data["currency"])
        if created:
            curr.save()

        opt, created = Optionee.objects.get_or_create(name=data["optionee_name"], iban=data["optionee_iban"], bic=data["optionee_bic"])
        if created:
            curr.save()

        transaction, created = Transaction.objects.get_or_create(
            account=acc,
            date_booking=data["date_booking"],
            optionee=opt,
            figure=data["figure"],
            currency=curr,
            reference=data["reference"],
        )
        if created:
            transaction.save()
